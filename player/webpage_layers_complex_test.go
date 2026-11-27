package main

// webpage_layers_complex_test.go covers the layering shapes a news front page
// leans on (CNN's card markup among them): a picture absolutely positioned
// inside a relative frame, a card whose text sits beside its picture in a
// grid, and a layer whose containing block holds nothing but layers.
//
// The fixtures are written in CSS pixels; layout runs at the harness's own
// text scale, so assertions compare boxes with each other (ratios, edges)
// rather than with the authored numbers.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// headlessLayerPicture is the one painted picture that lives in a layer: its
// page rectangle and the layer that paints it. A picture inside an absolutely
// positioned cover is a layer's picture, not a page one.
func headlessLayerPicture(h *headlessBrowser, layout *webLayout) (webFloat, sdl.Rect) {
	h.t.Helper()
	for _, f := range layout.Floats {
		for _, im := range f.Images {
			if im.Rect.W <= 0 || im.Rect.H <= 0 {
				continue
			}
			return f, sdl.Rect{X: f.X + im.Rect.X, Y: f.Y + im.Rect.Y, W: im.Rect.W, H: im.Rect.H}
		}
	}
	h.t.Fatalf("no layer paints a picture: floats=%d pageImages=%d", len(layout.Floats), len(layout.Images))
	return webFloat{}, sdl.Rect{}
}

// TestBrowserHeadlessAbsoluteImageStaysInItsFrame renders CNN's card idiom:
//
//	.frame{display:flex;position:relative;overflow:hidden;aspect-ratio:16/9}
//	.frame img{position:absolute;inset:0;width:100%;height:100%}
//
// The frame holds nothing in its flow, so it has no rows of its own. Its
// rectangle still exists — the card's grid gives it its width and the aspect
// ratio its height — and the absolutely positioned picture must be placed and
// clipped against it, never against the page: a picture anchored to the page
// is as wide as the whole column and covers the card's own headline.
func TestBrowserHeadlessAbsoluteImageStaysInItsFrame(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0}
		.card{display:grid;grid-template-columns:440px minmax(0,1fr);column-gap:20px;padding:20px;align-items:start}
		.card .text{grid-area:1/1}
		.card .media{grid-area:1/2}
		.frame{display:flex;position:relative;overflow:hidden;aspect-ratio:16/9;width:100%}
		.frame img{position:absolute;inset:0;width:100%;height:100%;object-fit:cover}
		.headline{display:block;font-size:28px;margin:0 0 10px}
		.desc{margin:0}
	</style>
	<div class="card">
		<div class="text"><span class="headline">Card headline words</span><p class="desc">Card summary text.</p></div>
		<div class="media"><div class="frame"><img src="` + headlessPhoto + `" alt="Card photo"></div></div>
	</div>`
	_, layout := h.load("card-media", html)
	raster := h.rasterize("card-media-frame")
	headline := h.textRectPrefix(layout, "Card")
	h.requireInside("headline", headline)
	layer, pic := headlessLayerPicture(h, layout)
	// The picture belongs to the card's second column, beside its text.
	if pic.X <= headline.X {
		t.Fatalf("card picture left its column: pic=%+v headline=%+v", pic, headline)
	}
	if pic.X+pic.W > h.pageBounds().W+2 {
		t.Fatalf("card picture runs off the page: %+v", pic)
	}
	if headlessOverlap(pic, headline, 6) {
		t.Fatalf("card picture covers the card's headline: pic=%+v headline=%+v", pic, headline)
	}
	// The frame's ratio sizes the picture: a one-line frame leaves a postage
	// stamp in the corner of the card.
	if ratio := float64(pic.H) / float64(pic.W); ratio < 0.45 || ratio > 0.68 {
		t.Fatalf("card picture is not the frame's 16:9 box: %+v (ratio %.2f)", pic, ratio)
	}
	// The frame clips it, as its overflow: hidden says.
	if !layer.HasClip {
		t.Fatalf("card picture is not clipped by its frame: %+v", layer)
	}
	if layer.Clip.W < pic.W-2 || layer.Clip.H < pic.H-2 || layer.Clip.W > pic.W+2 {
		t.Fatalf("card picture does not fill its frame's clip: pic=%+v clip=%+v", pic, layer.Clip)
	}
	photo, total := countPixels(raster, pic, headlessSaturatedPhoto)
	if photo*4 < total {
		t.Fatalf("card picture did not paint its pixels: %d/%d in %+v", photo, total, pic)
	}
}

// TestBrowserHeadlessLayerContainerKeepsItsBox covers a positioned container
// whose only child left the flow: a relative tile with an absolute cover
// picture and a caption layer inside it. The tile keeps a rectangle (its CSS
// height), so the caption at bottom: 0 sits at the tile's foot; it used to
// fall back to the page's corner.
func TestBrowserHeadlessLayerContainerKeepsItsBox(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0}
		.wrap{padding:20px}
		.tile{position:relative;width:400px;height:240px;overflow:hidden}
		.tile img{position:absolute;inset:0;width:100%;height:100%;object-fit:cover}
		.caption{position:absolute;left:0;bottom:0;right:0;padding:8px;background:#123;color:#fff}
		h2{margin:0 0 8px}
	</style>
	<div class="wrap">
		<h2>Following heading</h2>
		<div class="tile"><img src="` + headlessPhoto + `" alt="Tile photo"><div class="caption">Tile caption</div></div>
		<p>Text after the tile.</p>
	</div>`
	_, layout := h.load("layer-container", html)
	raster := h.rasterize("layer-container-box")
	layer, pic := headlessLayerPicture(h, layout)
	if !layer.HasClip {
		t.Fatalf("tile picture is not clipped by its overflow: hidden box: %+v", layer)
	}
	tile := layer.Clip
	caption, _ := h.floatTextRect(layout, "caption")
	h.requireInside("tile caption", caption)
	box := h.floatBoxAround(layout, "caption")
	// The caption spans its tile's width and sits on its foot.
	if box.W < tile.W-12 || box.W > tile.W+12 {
		t.Fatalf("caption is not as wide as its tile: caption=%+v tile=%+v picture=%+v", box, tile, pic)
	}
	if box.X < tile.X-12 || box.X > tile.X+12 {
		t.Fatalf("caption is not at its tile's left edge: caption=%+v tile=%+v", box, tile)
	}
	if foot := tile.Y + tile.H; box.Y+box.H < foot-12 || box.Y+box.H > foot+12 {
		t.Fatalf("caption is not at its tile's foot: caption=%+v tile=%+v", box, tile)
	}
	after := h.textRectPrefix(layout, "Text")
	if box.Y+box.H > after.Y+4 {
		t.Fatalf("caption runs into the text after the tile: caption=%+v after=%+v", box, after)
	}
	h.requireInk(raster, caption, "tile caption text")
}
