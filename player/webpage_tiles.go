package main

// webpage_tiles.go — the page painted once into GPU tiles, copied each frame.
//
// The reader painted the whole visible page on every frame: each surface,
// rounded corner, border, gradient, picture and text stretch was a draw call
// of its own — three to five hundred on a busy front page — and a blurred card
// shadow is a stack of up to eight translucent layers. On the A133P handhelds
// the PowerVR GE8300's driver spends CPU time on every draw call, on the same
// four Cortex-A53 cores the page's scripts, layout and picture decoding need
// while it loads, and the overdraw of those shadow stacks is fill rate the
// small GPU does not have to spare.
//
// On the handhelds the page is now painted into tiles: textures as wide as
// the page and webTileH (256) pixels tall, kept by the GPU. A frame copies the
// three or four the view covers — a scroll copies them from another offset —
// and draws only what moves on its own over them: position: fixed layers,
// sticky bars' pinned copies, a video playing in its card, then the link
// cursor and the find marks as before. A tile is painted again only when what
// it shows changes:
//   - a new layout (pictures or a web font sized the page, zoom, a form
//     edit), a new document (the finished page replacing its preview) or a
//     new page width throws every tile away;
//   - a tile painted while one of its pictures was still downloading or
//     decoding (webPaintPartial) is painted again when pictures land
//     (webPaintGen), at most every webTileRepaintMS, and every
//     webTileRetouchMS while it still waits, which keeps the decode asked for.
//
// The tiles around the view are painted ahead, one per frame, on frames that
// paint none in view, so a scroll finds them ready. Tiles that leave the
// window around the view are dropped first when the tune's count is reached
// (webTune.Tiles: 12 on the Smart Pro S, 10 on the A133P handhelds, about
// 1.3 MB each at the full width of 1280).
//
// A page the tiles cannot hold is painted directly, as before: one whose paper
// is a gradient (drawn across the view, not the page), one with a fixed layer
// under its content (the opaque tiles would cover it), and every page when
// the renderer cannot draw into a texture. A PC paints directly unless
// JUKAHUB_WEB_TILES asks for tiles (webpage_tune.go).

import (
	"sync/atomic"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// webTileH is a tile's height in layout pixels.
	webTileH = int32(256)
	// webTileRowLead is how many rows above a tile its painter starts from:
	// the glyphs of a large heading reach below the row they are laid out in.
	webTileRowLead = 6
	// webTileRepaintMS is the least time between two paints of a tile that is
	// waiting for pictures, while pictures land; webTileRetouchMS is how long
	// such a tile waits at most without any landing.
	webTileRepaintMS = 100
	webTileRetouchMS = 400
	// webTileThrashResets is how many times the tiles may be thrown away in
	// one second before the reader stops using them for the session: a page
	// that changes on every frame gains nothing from them.
	webTileThrashResets = 30
)

// webPaintGen counts the events that can complete a tile painted without
// something: a picture's bytes arriving, a decoded picture becoming a texture,
// an image pass ending. Any thread.
var webPaintGen atomic.Uint64

// webPaintPartial is set by a page draw that left out something still on its
// way — a picture downloading or decoding, a sharper decode of one — so the
// tile it was painting is painted again when that lands. Render thread.
var webPaintPartial bool

// webTile is one painted band of the page: layout pixels index*webTileH to
// (index+1)*webTileH.
type webTile struct {
	tex     *sdl.Texture
	index   int32
	partial bool
	gen     uint64
	painted uint64 // ticks of the paint
}

// webTiles is the tile cache of the page on screen. Render thread.
var webTiles struct {
	layout   *webLayout
	doc      *webDocument
	w        int32
	ok       bool // the layout can be tiled (webTileable)
	tiles    []*webTile
	painting bool // a tile is being painted (drawWebVideoInline keeps the card)
	failed   bool // the renderer refused a texture target: paint directly
	resets   int
	resetAt  uint64
}

// webTilesDraw draws the page through its tiles and reports whether it did;
// false leaves the frame to the direct painter. rect is the page on screen and
// offset its scroll in layout pixels. Render thread.
func webTilesDraw(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, offset int32) bool {
	limit := webTuneNow().Tiles
	if limit <= 0 || webTiles.failed || r == nil || doc == nil || layout == nil || rect.W <= 0 || rect.H <= 0 {
		return false
	}
	if webTiles.layout != layout || webTiles.doc != doc || webTiles.w != rect.W {
		webTilesReset()
		if webTilesThrashing() {
			return false
		}
		webTiles.layout, webTiles.doc, webTiles.w = layout, doc, rect.W
		webTiles.ok = webTileable(doc, layout) && r.RenderTargetSupported()
	}
	if !webTiles.ok {
		return false
	}
	first := offset / webTileH
	last := (offset + rect.H - 1) / webTileH
	// The view's own tiles always fit, whatever the tune says.
	capacity := max(limit, int(last-first)+2)
	now := sdl.GetTicks64()
	gen := webPaintGen.Load()
	painted := false
	for k := first; k <= last; k++ {
		t := webTileFind(k)
		if t != nil && !webTileStale(t, gen, now) {
			continue
		}
		if t == nil {
			if t = webTileMake(r, k, rect.W, capacity, first, last); t == nil {
				return false
			}
		}
		if !webTilePaint(r, config, doc, layout, t, rect.W) {
			return false
		}
		painted = true
	}
	if !painted {
		// A quiet frame paints one tile ahead: below the view, then above.
		pageH := max(layout.Height, int32(len(layout.Rows))*layout.RowH)
		for _, k := range [...]int32{last + 1, first - 1, last + 2} {
			if k < 0 || k*webTileH >= pageH || webTileFind(k) != nil {
				continue
			}
			if len(webTiles.tiles) >= capacity && !webTileEvictable(first, last, k) {
				break
			}
			if t := webTileMake(r, k, rect.W, capacity, first, last); t != nil {
				if !webTilePaint(r, config, doc, layout, t, rect.W) {
					return false
				}
			}
			break
		}
	}
	// The copies: opaque, a band of each tile the view covers.
	for k := first; k <= last; k++ {
		t := webTileFind(k)
		if t == nil {
			return false
		}
		top := k * webTileH
		y0 := max(offset, top)
		y1 := min(offset+rect.H, top+webTileH)
		if y1 <= y0 {
			continue
		}
		src := sdl.Rect{X: 0, Y: y0 - top, W: rect.W, H: y1 - y0}
		dst := sdl.Rect{X: rect.X, Y: rect.Y + y0 - offset, W: rect.W, H: y1 - y0}
		_ = r.Copy(t.tex, &src, &dst)
	}
	// What moves on its own goes over the tiles, every frame.
	renderWithClip(r, rect.X, rect.Y, rect.W, rect.H, func(rr *sdl.Renderer) {
		drawWebVideoInlineOver(rr, config, layout, rect, offset)
		drawWebTopLayersPart(rr, config, doc, layout, rect, offset, true)
	})
	return true
}

// webTileable reports whether the page can be drawn from tiles: its paper is
// one colour (a gradient paper is painted across the view, not the page) and
// no fixed layer sits under its content, where opaque tiles would hide it.
func webTileable(doc *webDocument, layout *webLayout) bool {
	if doc.HasPaperGradient || len(layout.Rows) == 0 {
		return false
	}
	for i := range layout.Floats {
		if f := &layout.Floats[i]; f.Pin != webPinNone && f.Phase != webLayerAbove {
			return false
		}
	}
	return true
}

// webTileStale reports whether a tile painted without something still on its
// way should be painted again now.
func webTileStale(t *webTile, gen, now uint64) bool {
	if !t.partial {
		return false
	}
	since := now - t.painted
	return (gen != t.gen && since >= webTileRepaintMS) || since >= webTileRetouchMS
}

// webTileFind returns the tile with index k, or nil.
func webTileFind(k int32) *webTile {
	for _, t := range webTiles.tiles {
		if t.index == k {
			return t
		}
	}
	return nil
}

// webTileEvictable reports whether a tile may be dropped to make room for
// tile k: one outside the view, farther from it than k is.
func webTileEvictable(first, last, k int32) bool {
	far := webTileDistance(k, first, last)
	for _, t := range webTiles.tiles {
		if t.index < first || t.index > last {
			if webTileDistance(t.index, first, last) > far {
				return true
			}
		}
	}
	return false
}

// webTileDistance is how many tiles k lies outside the view's tiles.
func webTileDistance(k, first, last int32) int32 {
	switch {
	case k < first:
		return first - k
	case k > last:
		return k - last
	}
	return 0
}

// webTileMake returns a texture for tile k: a new one while the cache has
// room, else the one farthest outside the view, reused. nil when the renderer
// cannot make one, which turns the tiles off for the session.
func webTileMake(r *sdl.Renderer, k, w int32, capacity int, first, last int32) *webTile {
	if len(webTiles.tiles) >= capacity {
		victim := -1
		var far int32 = -1
		for i, t := range webTiles.tiles {
			if t.index >= first && t.index <= last {
				continue
			}
			if d := webTileDistance(t.index, first, last); d > far {
				victim, far = i, d
			}
		}
		if victim >= 0 {
			t := webTiles.tiles[victim]
			t.index, t.partial, t.gen, t.painted = k, false, 0, 0
			return t
		}
	}
	// Linear filtering, fixed when the texture is made, as fitBackgroundTexture
	// does: a 1:1 copy looks the same either way, and on a panel the canvas is
	// scaled onto (the Brick) the page is smooth instead of blocky.
	prevQuality := sdl.GetHint(sdl.HINT_RENDER_SCALE_QUALITY)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "linear")
	tex, err := r.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_TARGET, w, webTileH)
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, prevQuality)
	if err != nil || tex == nil {
		webTilesFail("a page tile texture could not be made", err)
		return nil
	}
	// The page's paper is opaque: a tile replaces what is under it.
	_ = tex.SetBlendMode(sdl.BLENDMODE_NONE)
	t := &webTile{tex: tex, index: k}
	webTiles.tiles = append(webTiles.tiles, t)
	return t
}

// webTilePaint paints tile t: the page's paper and everything on it that
// moves with the scroll, as the direct painter draws it, into the texture.
// false when the renderer refused the texture as a target.
func webTilePaint(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, t *webTile, w int32) bool {
	prev := r.GetRenderTarget()
	// SDL puts the screen's clip back itself, but a texture target (a frame
	// being captured for a scene change) comes back unclipped.
	hadClip := r.IsClipEnabled()
	clip := r.GetClipRect()
	if err := r.SetRenderTarget(t.tex); err != nil {
		webTilesFail("the renderer cannot paint into a page tile", err)
		return false
	}
	webTiles.painting = true
	webPaintPartial = false
	// Whatever happens while it paints, the screen gets the renderer back: a
	// page whose drawing panics (the render pass recovers from that) must not
	// leave the rest of the frame drawing into a tile.
	defer func() {
		webTiles.painting = false
		_ = r.SetRenderTarget(prev)
		if hadClip {
			_ = r.SetClipRect(&clip)
		}
		_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		if rec := recover(); rec != nil {
			// The tile is half painted: none of them is kept.
			webTilesReset()
			panic(rec)
		}
	}()
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	// Read before the paint: a picture landing while it runs (an image pass
	// worker) still counts as news for the tile afterwards.
	gen := webPaintGen.Load()
	view := sdl.Rect{X: 0, Y: 0, W: w, H: webTileH}
	renderWithClip(r, view.X, view.Y, view.W, view.H, func(rr *sdl.Renderer) {
		drawWebPageContent(rr, config, doc, layout, view, t.index*webTileH, webPaintScrolling)
	})
	t.partial = webPaintPartial
	webPaintPartial = false
	t.gen = gen
	t.painted = sdl.GetTicks64()
	return true
}

// webTilesReset throws every tile away (a new layout, document or width, or
// the Browser letting its memory go). Render thread.
func webTilesReset() {
	for _, t := range webTiles.tiles {
		if t.tex != nil {
			_ = t.tex.Destroy()
		}
	}
	webTiles.tiles = nil
	webTiles.layout, webTiles.doc, webTiles.w, webTiles.ok = nil, nil, 0, false
}

// webTilesThrashing counts a reset and reports whether the tiles have been
// thrown away so often this second that they cost more than they save; it
// turns them off for the session then.
func webTilesThrashing() bool {
	now := sdl.GetTicks64()
	if now-webTiles.resetAt > 1000 {
		webTiles.resetAt, webTiles.resets = now, 0
	}
	webTiles.resets++
	if webTiles.resets > webTileThrashResets {
		webTilesFail("the page changes on every frame", nil)
		return true
	}
	return false
}

// webTilesFail turns the tiles off for the session; pages are painted
// directly from then on.
func webTilesFail(why string, err error) {
	webTilesReset()
	webTiles.failed = true
	if err != nil {
		logWarn("browser", "page tiles off, %s: %v", why, err)
		return
	}
	logWarn("browser", "page tiles off: %s", why)
}

// drawWebTopLayersPart paints one part of the stack over the flow
// (drawWebTopLayers): fixed false paints the layers that scroll with the page,
// into a tile; fixed true the ones drawn over the tiles each frame — layers
// pinned to the screen, sticky bars' pinned copies, and the layers a sticky
// bar carries (painted at their page place only while it is not pinned).
// Each part keeps the stacking order within itself.
func drawWebTopLayersPart(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, offset int32, fixed bool) {
	floats, stickies := layout.Floats, layout.Stickies
	if fixed {
		i, j := 0, 0
		for {
			for i < len(floats) && (floats[i].Phase != webLayerAbove || (floats[i].Pin == webPinNone && floats[i].StickyIndex <= 0)) {
				i++
			}
			haveF, haveS := i < len(floats), j < len(stickies)
			if !haveF && !haveS {
				return
			}
			if haveS && (!haveF || stickies[j].Key.less(floats[i].Key)) {
				drawWebStickyLayer(r, config, doc, layout, rect, &stickies[j], offset)
				j++
				continue
			}
			if s := floats[i].StickyIndex; s > 0 && s <= len(stickies) {
				if _, pinned := webStickyPinnedTop(&stickies[s-1], rect, offset); pinned {
					i++
					continue
				}
			}
			drawWebFloatLayer(r, config, doc, layout, rect, &floats[i], offset)
			i++
		}
	}
	for i := range floats {
		f := &floats[i]
		if f.Phase != webLayerAbove || f.Pin != webPinNone || f.StickyIndex > 0 {
			continue
		}
		drawWebFloatLayer(r, config, doc, layout, rect, f, offset)
	}
}
