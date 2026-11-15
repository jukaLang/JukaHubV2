package main

// webpage_headless_render_test.go exercises the page path the browser window
// uses, rather than a test-only parser shortcut:
//
//   parseWebPage (HTML + inline CSS)
//     -> fetchWebImages (the same bounded image pass as a page load)
//     -> setBrowserDocument/browserPageLayout (the window's page lookup and
//        cached layout)
//     -> drawWebDocument on a software SDL renderer (the window's painter)
//
// The layout's rows, rects, backgrounds, images, videos, floats and hit boxes
// are the scene the window consumes, so the fixtures assert against those
// objects directly. The software renderer also makes sure every fixture can be
// painted without a live window.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	headlessBrowserWidth  = 1000
	headlessBrowserHeight = 1600
	// A tiny valid GIF. Fixture images use data URIs so the headless pass never
	// needs the network, while still exercising the image fetch/decode path.
	headlessPixel = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=="
	// The PNGs are deliberately colourful: pixel assertions can tell them
	// apart from the page paper, a missing image placeholder and a gradient.
	headlessRenderDir = "testdata/headless-render"
)

var (
	headlessPhoto  = headlessImageURI(headlessQuadrantImage())
	headlessSwatch = headlessImageURI(headlessSolidImage(color.NRGBA{R: 224, G: 126, B: 42, A: 255}))
)

func headlessImageURI(img image.Image) string {
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		panic(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
}

func headlessQuadrantImage() image.Image {
	const side = 16
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			c := color.NRGBA{R: 220, G: 42, B: 42, A: 255}
			switch {
			case x >= side/2 && y < side/2:
				c = color.NRGBA{R: 36, G: 190, B: 76, A: 255}
			case x < side/2 && y >= side/2:
				c = color.NRGBA{R: 38, G: 82, B: 220, A: 255}
			case x >= side/2 && y >= side/2:
				c = color.NRGBA{R: 244, G: 194, B: 34, A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func headlessSolidImage(c color.NRGBA) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

var headlessSDL = struct {
	once sync.Once
	err  error
}{}

// headlessBrowser is one page rendered through the desktop browser's page path.
type headlessBrowser struct {
	t        *testing.T
	renderer *sdl.Renderer
	surface  *sdl.Surface
	config   *Config
	elem     Element
	width    int32 // element width, including the browser's 7px side gutters
}

func newHeadlessBrowser(t *testing.T) *headlessBrowser {
	t.Helper()
	legibleTestInitFonts(t)
	headlessSDL.once.Do(func() {
		headlessSDL.err = sdl.Init(sdl.INIT_VIDEO)
	})
	if headlessSDL.err != nil {
		t.Fatalf("initialize SDL video for headless renderer: %v", headlessSDL.err)
	}
	surface, err := sdl.CreateRGBSurface(0, headlessBrowserWidth, headlessBrowserHeight, 32, 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("create headless surface: %v", err)
	}
	renderer, err := sdl.CreateSoftwareRenderer(surface)
	if err != nil {
		surface.Free()
		t.Fatalf("create software renderer: %v", err)
	}

	oldZoom, oldStrip := browserZoomIndex, browserCookieStripReserved
	browserZoomIndex, browserCookieStripReserved = browserZoomDefault, 0
	// Text and image textures are cached globally and belong to the renderer
	// that made them. This harness creates one renderer per test, so a texture
	// left by an earlier test points at a destroyed renderer: later Copy calls
	// fail silently and words or images shared with an earlier fixture vanish
	// from the raster. Flush both caches before and after each page.
	GetTextCache().Clear()
	clearThumbnailCache()
	t.Cleanup(func() {
		browserZoomIndex, browserCookieStripReserved = oldZoom, oldStrip
		GetTextCache().Clear()
		clearThumbnailCache()
		renderer.Destroy()
		surface.Free()
	})

	return &headlessBrowser{
		t: t, renderer: renderer, surface: surface, config: &Config{},
		elem: Element{
			Font:   "body",
			Width:  StringOrInt(strconv.Itoa(headlessBrowserWidth)),
			Height: StringOrInt(strconv.Itoa(headlessBrowserHeight)),
		},
		width: headlessBrowserWidth,
	}
}

// pageBounds is the layout-local content box of the browser page: the same
// 7px gutter renderTextBrowser leaves on each side of the element.
func (h *headlessBrowser) pageBounds() sdl.Rect {
	return sdl.Rect{X: 0, Y: 0, W: h.width - 14, H: headlessBrowserHeight}
}

// load parses html, runs the image pass, stores it exactly where a page load
// stores it, asks the browser for the laid-out page, and paints that page.
func (h *headlessBrowser) load(name, html string) (*webDocument, *webLayout) {
	return h.loadAt(name, html, "https://fixture.jukahub.test/"+name)
}

func (h *headlessBrowser) loadAt(name, html, baseURL string) (*webDocument, *webLayout) {
	h.t.Helper()
	doc := parseWebPage(html, "", baseURL)
	if doc == nil {
		h.t.Fatalf("%s: parseWebPage returned nil", name)
	}
	fetchWebImages(doc)
	variable := "headless_browser_" + name
	h.elem.Variable = variable
	setBrowserDocument(variable, doc)
	h.t.Cleanup(func() { setBrowserDocument(variable, nil) })

	layout := browserPageLayout(h.config, h.elem)
	if layout == nil || len(layout.Rows) == 0 {
		h.t.Fatalf("%s: browserPageLayout returned no rows (doc blocks=%d)", name, len(doc.Blocks))
	}
	h.paintPage(doc, layout, 0)
	return doc, layout
}

// paintPage clears the software target and runs the desktop browser's draw
// call at startRow. Clearing matters for the pixel pass: the page clip owns
// only its own rectangle, so the gutter must be paper rather than uninitialised
// surface memory when a fixture is rasterized.
func (h *headlessBrowser) paintPage(doc *webDocument, layout *webLayout, startRow int) {
	h.t.Helper()
	h.renderer.SetDrawColor(255, 255, 255, 255)
	h.renderer.Clear()
	page := h.pageBounds()
	drawWebDocument(h.renderer, h.config, doc, layout, sdl.Rect{X: 7, Y: 0, W: page.W, H: page.H}, startRow)
	h.renderer.Present()
}

// rasterize reads the software renderer's surface back and writes the exact
// pixels drawWebDocument produced. The output directory is gitignored; the
// PNGs are diagnostics as well as the substrate for the pixel assertions.
func (h *headlessBrowser) rasterize(name string) *image.NRGBA {
	h.t.Helper()
	h.renderer.Present()
	conv, err := h.surface.ConvertFormat(sdl.PIXELFORMAT_ABGR8888, 0)
	if err != nil || conv == nil {
		h.t.Fatalf("%s: convert headless surface: %v", name, err)
	}
	defer conv.Free()
	w, hgt, pitch := int(conv.W), int(conv.H), int(conv.Pitch)
	src := conv.Pixels()
	if w <= 0 || hgt <= 0 || pitch < w*4 || len(src) < pitch*hgt {
		h.t.Fatalf("%s: invalid headless surface %dx%d pitch=%d bytes=%d", name, w, hgt, pitch, len(src))
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, hgt))
	for y := 0; y < hgt; y++ {
		copy(img.Pix[y*img.Stride:y*img.Stride+w*4], src[y*pitch:y*pitch+w*4])
	}
	if err := os.MkdirAll(headlessRenderDir, 0o755); err != nil {
		h.t.Fatalf("%s: create PNG directory: %v", name, err)
	}
	path := filepath.Join(headlessRenderDir, name+".png")
	file, err := os.Create(path)
	if err != nil {
		h.t.Fatalf("%s: create PNG: %v", name, err)
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		h.t.Fatalf("%s: encode PNG: %v", name, err)
	}
	if err := file.Close(); err != nil {
		h.t.Fatalf("%s: close PNG: %v", name, err)
	}
	h.t.Logf("rasterized %s -> %s", name, path)
	return img
}

func headlessPixelAt(img *image.NRGBA, x, y int32) color.NRGBA {
	if img == nil || x < 0 || y < 0 || int(x) >= img.Bounds().Dx() || int(y) >= img.Bounds().Dy() {
		return color.NRGBA{}
	}
	return img.NRGBAAt(int(x), int(y))
}

func headlessNear(c color.NRGBA, want sdl.Color, tolerance int) bool {
	d := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	return d(c.R, want.R) <= tolerance && d(c.G, want.G) <= tolerance && d(c.B, want.B) <= tolerance
}

// requireFill counts pixels of an expected fill over a substantial part of a
// region. A missing background leaves the page paper there, so the exact fill
// count collapses even though the box geometry still looks correct.
func (h *headlessBrowser) requireFill(img *image.NRGBA, r sdl.Rect, want sdl.Color, minShare float64, label string) {
	h.t.Helper()
	r = headlessInset(r, 4)
	if r.W <= 0 || r.H <= 0 {
		h.t.Fatalf("%s: fill region collapsed: %+v", label, r)
	}
	matched, total := 0, 0
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			total++
			if headlessNear(headlessPixelAt(img, x, y), want, 12) {
				matched++
			}
		}
	}
	share := float64(matched) / float64(total)
	if share < minShare {
		h.t.Fatalf("%s: expected fill %v over %.0f%% of %+v, painted %.1f%% (%d/%d pixels)", label, want, minShare*100, r, share*100, matched, total)
	}
}

// requireInk proves a text run painted glyphs inside its measured box. The
// dominant quantised colour is the local background; glyph antialiasing and
// the glyph core are the pixels far from it.
func (h *headlessBrowser) requireInk(img *image.NRGBA, r sdl.Rect, label string) {
	h.t.Helper()
	r = headlessInset(r, -2)
	if r.W <= 0 || r.H <= 0 {
		h.t.Fatalf("%s: text box collapsed: %+v", label, r)
	}
	type key struct{ r, g, b uint8 }
	counts := map[key]int{}
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			c := headlessPixelAt(img, x, y)
			counts[key{c.R >> 4, c.G >> 4, c.B >> 4}]++
		}
	}
	var bg key
	best := -1
	for c, n := range counts {
		if n > best {
			bg, best = c, n
		}
	}
	ink := 0
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			c := headlessPixelAt(img, x, y)
			delta := max(abs(int(c.R)-int(bg.r)*16), max(abs(int(c.G)-int(bg.g)*16), abs(int(c.B)-int(bg.b)*16)))
			if delta > 55 {
				ink++
			}
		}
	}
	if ink < 8 || ink*40 < int(r.W*r.H) {
		h.t.Fatalf("%s: text run painted too little ink in %+v (%d pixels; background bin %v)", label, r, ink, bg)
	}
}

// requireClearRightGutter catches text or a box leaking through the page's
// right clip. The page painter fills only its content rectangle, so anything
// non-paper in the gutter is a real overflow rather than a cleared surface.
func (h *headlessBrowser) requireClearRightGutter(img *image.NRGBA, label string) {
	h.t.Helper()
	page := h.pageBounds()
	start := int32(7) + page.W
	if start >= int32(img.Bounds().Dx()) {
		h.t.Fatalf("%s: page right edge %d is outside the raster", label, start)
	}
	for y := int32(0); y < int32(img.Bounds().Dy()); y++ {
		for x := start; x < int32(img.Bounds().Dx()); x++ {
			c := headlessPixelAt(img, x, y)
			if c.R < 250 || c.G < 250 || c.B < 250 {
				h.t.Fatalf("%s: painted pixel %v at (%d,%d), past the page's right content edge %d", label, c, x, y, start)
			}
		}
	}
}

func headlessInset(r sdl.Rect, n int32) sdl.Rect {
	return sdl.Rect{X: r.X + n, Y: r.Y + n, W: r.W - 2*n, H: r.H - 2*n}
}

func (h *headlessBrowser) requireTextPixels(img *image.NRGBA, layout *webLayout, text, label string) sdl.Rect {
	h.t.Helper()
	r := h.textRect(layout, text)
	h.requireInk(img, r, label+" "+text)
	return r
}

func headlessGradientBox(layout *webLayout) sdl.Rect {
	best := sdl.Rect{}
	bestArea := int32(0)
	for _, bg := range layout.Backgrounds {
		if !bg.HasGradient || bg.Rect.W <= 0 || bg.Rect.H <= 0 {
			continue
		}
		if area := bg.Rect.W * bg.Rect.H; area > bestArea {
			best, bestArea = bg.Rect, area
		}
	}
	return best
}

func headlessImageBackgroundBox(layout *webLayout) sdl.Rect {
	best := sdl.Rect{}
	bestArea := int32(0)
	for _, bg := range layout.Backgrounds {
		if bg.Image == "" || bg.Rect.W <= 0 || bg.Rect.H <= 0 {
			continue
		}
		if area := bg.Rect.W * bg.Rect.H; area > bestArea {
			best, bestArea = bg.Rect, area
		}
	}
	return best
}

// countPixels counts pixels in r for which match is true.
func countPixels(img *image.NRGBA, r sdl.Rect, match func(color.NRGBA) bool) (matched, total int) {
	r = headlessInset(r, 2)
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			total++
			if match(headlessPixelAt(img, x, y)) {
				matched++
			}
		}
	}
	return matched, total
}

func headlessSaturatedPhoto(c color.NRGBA) bool {
	red := int(c.R) > int(c.G)+30 && int(c.R) > int(c.B)+30 && c.R > 70
	green := int(c.G) > int(c.R)+30 && int(c.G) > int(c.B)+20 && c.G > 70
	return red || green
}

func headlessOrange(c color.NRGBA) bool {
	return int(c.R) > int(c.G)+20 && int(c.G) > int(c.B)+15 && c.R > 90
}

// textRect returns the measured scene box of one exact run. A missing run is a
// test failure: an element that vanishes is the defect these fixtures guard.
func (h *headlessBrowser) textRect(layout *webLayout, text string) sdl.Rect {
	h.t.Helper()
	rect, ok := headlessTextRect(layout, text)
	if !ok {
		h.t.Fatalf("layout has no text run %q; runs=%v", text, headlessRunTexts(layout))
	}
	return rect
}

// floatTextRect is textRect for an absolutely positioned layer.
func (h *headlessBrowser) floatTextRect(layout *webLayout, text string) (sdl.Rect, int) {
	h.t.Helper()
	for i, f := range layout.Floats {
		for _, row := range f.Rows {
			for _, run := range row.Runs {
				if run.Text == text && run.Font != nil {
					w, h, err := run.Font.SizeUTF8(run.Text)
					if err != nil {
						continue
					}
					return sdl.Rect{X: f.X + run.X, Y: f.Y + row.Y, W: int32(w), H: int32(h)}, i
				}
			}
		}
	}
	h.t.Fatalf("layout has no float text run %q (floats=%d) runs=%v", text, len(layout.Floats), headlessRunTexts(layout))
	return sdl.Rect{}, -1
}

// floatBoxAround is boxAround for an absolutely positioned layer.
func (h *headlessBrowser) floatBoxAround(layout *webLayout, text string) sdl.Rect {
	h.t.Helper()
	run, index := h.floatTextRect(layout, text)
	if index < 0 || index >= len(layout.Floats) {
		h.t.Fatalf("float text %q has no layer", text)
	}
	best := sdl.Rect{}
	bestArea := int32(1<<30 - 1)
	for _, bg := range layout.Floats[index].Backgrounds {
		box := sdl.Rect{X: layout.Floats[index].X + bg.Rect.X, Y: layout.Floats[index].Y + bg.Rect.Y, W: bg.Rect.W, H: bg.Rect.H}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		if run.X < box.X || run.Y < box.Y ||
			run.X+run.W > box.X+box.W || run.Y+run.H > box.Y+box.H {
			continue
		}
		if area := box.W * box.H; area < bestArea {
			best, bestArea = box, area
		}
	}
	if bestArea == int32(1<<30-1) {
		h.t.Fatalf("no painted float background contains %q at %+v", text, run)
	}
	return best
}

// boxAround finds the smallest painted block box that contains a text run. It
// is how the fixtures get a card's actual background bounds without depending
// on a particular CSS colour or parser block index.
func (h *headlessBrowser) boxAround(layout *webLayout, text string) sdl.Rect {
	h.t.Helper()
	run := h.textRect(layout, text)
	best := sdl.Rect{}
	bestArea := int32(1<<30 - 1)
	for _, r := range layout.Rects {
		if !r.BackgroundFill || r.Rect.W <= 0 || r.Rect.H <= 0 {
			continue
		}
		if run.X < r.Rect.X || run.Y < r.Rect.Y ||
			run.X+run.W > r.Rect.X+r.Rect.W || run.Y+run.H > r.Rect.Y+r.Rect.H {
			continue
		}
		if area := r.Rect.W * r.Rect.H; area < bestArea {
			best, bestArea = r.Rect, area
		}
	}
	if bestArea == int32(1<<30-1) {
		fills := make([]string, 0, len(layout.Rects))
		for _, r := range layout.Rects {
			if r.BackgroundFill {
				fills = append(fills, fmt.Sprintf("%+v", r.Rect))
			}
		}
		h.t.Fatalf("no painted background box contains %q at %+v; fills=%v backgrounds=%v", text, run, fills, layout.Backgrounds)
	}
	return best
}

func headlessRunTexts(layout *webLayout) []string {
	if layout == nil {
		return nil
	}
	texts := make([]string, 0, 32)
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text != "" {
				texts = append(texts, run.Text)
			}
		}
	}
	for i, f := range layout.Floats {
		for _, row := range f.Rows {
			for _, run := range row.Runs {
				if run.Text != "" {
					texts = append(texts, fmt.Sprintf("float%d:%s", i, run.Text))
				}
			}
		}
	}
	return texts
}

func headlessTextRect(layout *webLayout, text string) (sdl.Rect, bool) {
	if layout == nil {
		return sdl.Rect{}, false
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text != text || run.Font == nil {
				continue
			}
			w, h, err := run.Font.SizeUTF8(run.Text)
			if err != nil {
				continue
			}
			return sdl.Rect{X: run.X, Y: row.Y, W: int32(w), H: int32(h)}, true
		}
	}
	return sdl.Rect{}, false
}

func (h *headlessBrowser) requireInside(label string, r sdl.Rect) {
	h.t.Helper()
	page := h.pageBounds()
	if r.W <= 0 || r.H <= 0 {
		h.t.Fatalf("%s has a collapsed box: %+v", label, r)
	}
	// A block's background extends four design pixels beyond its text box for
	// the reader's painted edge. Permit that only as a small gutter overhang.
	if r.X < -4 || r.Y < -4 || r.X+r.W > page.W+4 {
		h.t.Fatalf("%s is outside the page content width %d: %+v", label, page.W, r)
	}
}

func headlessOverlap(a, b sdl.Rect, tolerance int32) bool {
	return a.X < b.X+b.W-tolerance && b.X < a.X+a.W-tolerance &&
		a.Y < b.Y+b.H-tolerance && b.Y < a.Y+a.H-tolerance
}

func TestBrowserHeadlessHarnessPaintsRealPagePath(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>h1{color:#123}</style><main><h1>Harness title</h1><p>The page path is real.</p></main>`
	doc, layout := h.load("harness", html)
	img := h.rasterize("harness-real-page-path")
	if doc.Title != "" {
		// No <title>: the browser fills one from the URL elsewhere; this only
		// verifies the parsed document is the one stored for the variable.
		t.Fatalf("unexpected parsed title %q", doc.Title)
	}
	title := h.textRect(layout, "Harness")
	h.requireInside("harness heading", title)
	body := h.textRect(layout, "real.")
	h.requireInside("harness body", body)
	if body.Y <= title.Y {
		t.Fatalf("body should follow the heading: title=%+v body=%+v", title, body)
	}
	h.requireTextPixels(img, layout, "Harness", "harness heading")
	h.requireTextPixels(img, layout, "real.", "harness body")
	h.requireClearRightGutter(img, "harness")
}

func TestBrowserHeadlessFlexCardGrid(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0}
		.cards{display:flex;flex-wrap:wrap;gap:18px;align-items:stretch;padding:12px}
		.card{flex:1 1 160px;min-height:150px;padding:18px;background:#eef;border:1px solid #778;border-radius:12px}
		.card h3{margin:0 0 8px;font-size:20px}
		.card p{margin:0}
	</style>
	<section class="cards">
		<article class="card"><h3>Alpha</h3><p>First card body text.</p></article>
		<article class="card"><h3>Beta</h3><p>Second card body text.</p></article>
		<article class="card"><h3>Gamma</h3><p>Third card body text.</p></article>
	</section>`
	_, layout := h.load("flex-cards", html)
	img := h.rasterize("flex-card-grid")
	labels := []string{"Alpha", "Beta", "Gamma"}
	boxes := make([]sdl.Rect, 0, len(labels))
	for _, label := range labels {
		text := h.textRect(layout, label)
		h.requireInside(label+" text", text)
		box := h.boxAround(layout, label)
		h.requireInside(label+" card", box)
		if box.W < 120 || box.H < 100 {
			t.Fatalf("%s card collapsed to %+v", label, box)
		}
		h.requireFill(img, box, sdl.Color{R: 238, G: 238, B: 255, A: 255}, 0.35, label+" card")
		h.requireTextPixels(img, layout, label, "card heading")
		boxes = append(boxes, box)
	}
	for i := 0; i < len(boxes); i++ {
		for j := i + 1; j < len(boxes); j++ {
			if headlessOverlap(boxes[i], boxes[j], 8) {
				t.Fatalf("cards overlap: %s=%+v %s=%+v", labels[i], boxes[i], labels[j], boxes[j])
			}
		}
	}
	if !(boxes[0].X < boxes[1].X && boxes[1].X < boxes[2].X) {
		t.Fatalf("card order is not left-to-right: %+v", boxes)
	}
	h.requireClearRightGutter(img, "flex cards")
}

func TestBrowserHeadlessCSSGridSidebar(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0}
		.page{display:grid;grid-template-columns:240px 1fr;gap:24px;align-items:start;padding:16px}
		.sidebar{background:#eef;padding:16px;border:1px solid #99a}
		.main{background:#fff;padding:16px;border:1px solid #ccd}
		h1{font-size:28px;margin:0 0 10px}
		p{margin:0 0 10px}
	</style>
	<div class="page">
		<aside class="sidebar"><h2>Sidebar</h2><p>Navigation</p></aside>
		<main class="main"><h1>Main content</h1><p>This is the article column beside the sidebar.</p></main>
	</div>`
	_, layout := h.load("grid-sidebar", html)
	img := h.rasterize("grid-sidebar")
	sideText := h.textRect(layout, "Sidebar")
	mainText := h.textRect(layout, "Main")
	h.requireInside("sidebar text", sideText)
	h.requireInside("main text", mainText)
	if mainText.X <= sideText.X {
		t.Fatalf("grid items are not in separate columns: side=%+v main=%+v", sideText, mainText)
	}
	sideBox := h.boxAround(layout, "Sidebar")
	mainBox := h.boxAround(layout, "Main")
	h.requireInside("sidebar box", sideBox)
	h.requireInside("main box", mainBox)
	if sideBox.W >= mainBox.W {
		t.Fatalf("240px sidebar should be narrower than the 1fr main column: side=%+v main=%+v", sideBox, mainBox)
	}
	if sideBox.X+sideBox.W > mainBox.X+8 {
		t.Fatalf("sidebar overlaps the main column: side=%+v main=%+v", sideBox, mainBox)
	}
	h.requireFill(img, sideBox, sdl.Color{R: 238, G: 238, B: 255, A: 255}, 0.35, "sidebar")
	h.requireTextPixels(img, layout, "Sidebar", "sidebar heading")
	h.requireTextPixels(img, layout, "Main", "main heading")
	h.requireClearRightGutter(img, "grid sidebar")
}

func TestBrowserHeadlessStickyHeaderAndOverlay(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0}
		.sticky{position:sticky;top:0;z-index:5;background:#123;color:#fff;padding:10px 14px}
		.hero{position:relative;min-height:220px;padding:24px;background:#f4f6fa}
		.hero h1{margin:0 0 8px}
		.overlay{position:absolute;top:20px;right:20px;width:260px;padding:14px;background:#fff;border:2px solid #159;border-radius:10px;z-index:10}
		article p{margin:0 0 10px}
	</style>
	<main>
	<header class="sticky">Sticky site header</header>
	<section class="hero"><h1>Hero heading</h1><p>Hero summary text.</p>
		<div class="overlay"><strong>Overlay card</strong><p>Overlay detail.</p></div>
	</section>
	<article><p>Story text begins here.</p><p>More story text follows.</p></article>
	</main>`
	doc, layout := h.load("sticky-overlay", html)
	img := h.rasterize("sticky-overlay")
	header := h.textRect(layout, "Sticky")
	if header.W <= 0 || header.H <= 0 {
		t.Fatalf("sticky header text collapsed: %+v", header)
	}
	headerBox := h.boxAround(layout, "Sticky")
	h.requireInside("sticky header", headerBox)
	overlay, _ := h.floatTextRect(layout, "Overlay")
	h.requireInside("overlay text", overlay)
	if len(layout.Floats) != 1 {
		t.Fatalf("expected the absolute overlay as one float, got %d floats", len(layout.Floats))
	}
	if len(layout.Stickies) != 1 {
		t.Fatalf("expected one sticky overlay, got %d", len(layout.Stickies))
	}
	if len(layout.Floats[0].Backgrounds) == 0 {
		t.Fatalf("overlay lost its background layer: %+v", layout.Floats[0])
	}
	story := h.textRect(layout, "Story")
	if headlessOverlap(overlay, story, 8) {
		t.Fatalf("overlay overlaps separate story content: overlay=%+v story=%+v", overlay, story)
	}
	if headerBox.Y+headerBox.H > h.textRect(layout, "Hero").Y+8 {
		t.Fatalf("sticky header overlaps the hero below it: header=%+v hero=%+v", headerBox, h.textRect(layout, "Hero"))
	}
	h.requireFill(img, headerBox, sdl.Color{R: 17, G: 34, B: 51, A: 255}, 0.25, "sticky header")
	overlayBox := h.floatBoxAround(layout, "Overlay")
	h.requireFill(img, overlayBox, sdl.Color{R: 255, G: 255, B: 255, A: 255}, 0.25, "absolute overlay")
	h.requireInk(img, overlay, "overlay heading")
	h.requireClearRightGutter(img, "sticky overlay")

	// Scroll past the header's flow position. Its normal rows disappear above
	// the viewport; the pinned copy must still paint the same surface at top.
	h.paintPage(doc, layout, 8)
	scrolled := h.rasterize("sticky-overlay-scrolled")
	pinned := sdl.Rect{X: headerBox.X, Y: 0, W: headerBox.W, H: headerBox.H + 8}
	h.requireFill(scrolled, pinned, sdl.Color{R: 17, G: 34, B: 51, A: 255}, 0.25, "pinned sticky header")
	h.requireInk(scrolled, sdl.Rect{X: header.X, Y: 0, W: header.W, H: header.H + 8}, "pinned sticky text")
	h.requireClearRightGutter(scrolled, "scrolled sticky overlay")
}

func TestBrowserHeadlessHeroBackgroundLayers(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0}
		.hero{min-height:300px;padding:40px;color:#fff;background-image:linear-gradient(rgba(10,30,60,.8),rgba(10,30,60,.4)),url(` + headlessSwatch + `);background-size:cover;background-repeat:no-repeat}
		.hero h1{margin:0;font-size:34px}
		.hero p{max-width:420px}
	</style>
	<section class="hero"><h1>Launch something</h1><p>A hero needs its background, its heading and its copy.</p></section>
	<p>Text after the hero.</p>`
	doc, layout := h.load("hero", html)
	img := h.rasterize("hero-background-layers")
	title := h.textRect(layout, "Launch")
	h.requireInside("hero heading", title)
	gradients, images := 0, 0
	for _, bg := range layout.Backgrounds {
		if bg.HasGradient {
			gradients++
		}
		if bg.Image != "" {
			images++
		}
	}
	if gradients == 0 || images == 0 {
		blocks := make([]string, 0, len(doc.Blocks))
		for _, b := range doc.Blocks {
			text := ""
			if len(b.Runs) > 0 {
				text = b.Runs[0].Text
			}
			blocks = append(blocks, fmt.Sprintf("%q bg=%v layers=%d", text, b.HasBg, b.Style.BackgroundLayerCount))
		}
		t.Fatalf("hero background layers missing: gradients=%d images=%d backgrounds=%+v blocks=%v", gradients, images, layout.Backgrounds, blocks)
	}
	after := h.textRect(layout, "after")
	if after.Y <= title.Y {
		t.Fatalf("content after the hero did not follow it: hero=%+v after=%+v", title, after)
	}
	gradientBox := headlessGradientBox(layout)
	imageBox := headlessImageBackgroundBox(layout)
	if gradientBox.W <= 0 || imageBox.W <= 0 {
		t.Fatalf("hero did not produce both background layers: gradient=%+v image=%+v", gradientBox, imageBox)
	}
	orange, total := countPixels(img, imageBox, headlessOrange)
	if orange*10 < total {
		t.Fatalf("hero image layer is not visibly painted: %d/%d orange pixels in %+v", orange, total, imageBox)
	}
	// The two gradient stops must produce a real luminance ramp, not one flat
	// average colour. Sample the top and bottom thirds away from the copy.
	topThird := sdl.Rect{X: gradientBox.X + 6, Y: gradientBox.Y + 6, W: gradientBox.W - 12, H: gradientBox.H / 3}
	bottomThird := sdl.Rect{X: gradientBox.X + 6, Y: gradientBox.Y + gradientBox.H*2/3, W: gradientBox.W - 12, H: gradientBox.H / 3}
	topAvg := headlessAverageLuminance(img, topThird)
	bottomAvg := headlessAverageLuminance(img, bottomThird)
	if bottomAvg-topAvg < 12 {
		t.Fatalf("hero gradient is not visible: top luminance %.1f bottom %.1f", topAvg, bottomAvg)
	}
	h.requireTextPixels(img, layout, "Launch", "hero heading")
	h.requireTextPixels(img, layout, "after", "post-hero copy")
	h.requireClearRightGutter(img, "hero")
}

func headlessAverageLuminance(img *image.NRGBA, r sdl.Rect) float64 {
	total, n := 0, 0
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			c := headlessPixelAt(img, x, y)
			total += (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(total) / float64(n)
}

func TestBrowserHeadlessLongArticleImageAndList(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0}
		.article{max-width:620px;margin:0 auto;padding:18px}
		.article h1{font-size:30px;margin:0 0 14px}
		.article p{margin:0 0 14px;line-height:1.5}
		.article img{width:320px;height:180px;border-radius:8px}
		.article ul{margin:0 0 14px 0;padding-left:24px}
		.article li{margin:4px 0}
	</style>
	<article class="article">
		<h1>Long article</h1>
		<p>` + strings.Repeat("A long article paragraph with several words that should wrap cleanly. ", 10) + `</p>
		<img src="` + headlessPhoto + `" alt="Article photo" width="320" height="180">
		<ul><li>First list item</li><li>Second list item</li><li>Third list item</li></ul>
		<p>Final paragraph with a supercalifragilisticexpialidociousunbrokenwordthatmustnotrunoffthepage.</p>
	</article>`
	_, layout := h.load("article", html)
	raster := h.rasterize("long-article-image-list")
	if len(layout.Images) != 1 {
		t.Fatalf("article image was dropped: images=%+v", layout.Images)
	}
	img := layout.Images[0].Rect
	if img.W <= 0 || img.H <= 0 {
		t.Fatalf("article image has a collapsed box: %+v", img)
	}
	h.requireInside("article image", img)
	photo, total := countPixels(raster, img, headlessSaturatedPhoto)
	if photo*4 < total {
		t.Fatalf("article image did not paint its pixels: %d/%d saturated in %+v", photo, total, img)
	}
	for _, label := range []string{"First", "Second", "Third", "Final"} {
		r := h.textRect(layout, label)
		h.requireInside("article "+label, r)
		if r.X+r.W > h.pageBounds().W-18 {
			t.Fatalf("article text %q runs off the content area: %+v", label, r)
		}
		h.requireTextPixels(raster, layout, label, "article list/copy")
	}
	// No run may paint past the right edge of the page. This is the overflow
	// invariant the reader's word wrapping is responsible for.
	page := h.pageBounds()
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Font == nil || run.Text == "" {
				continue
			}
			w, _, err := run.Font.SizeUTF8(run.Text)
			if err == nil && run.X+int32(w) > page.W+2 {
				t.Fatalf("run %q overflows the page: x=%d width=%d page=%d", run.Text, run.X, w, page.W)
			}
		}
	}
	h.requireClearRightGutter(raster, "article")
}

func TestBrowserHeadlessTableAndForm(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0}
		table{width:100%;border-collapse:collapse;margin:0 0 18px}
		th,td{border:1px solid #99a;padding:8px;text-align:left}
		th{background:#eef}
		tbody td{background:#f2f7ff}
		form{padding:16px;background:#f6f8fb;border:1px solid #ccd}
		label{display:block;margin:8px 0 4px}
		input,textarea{width:260px;padding:6px;border:1px solid #99a}
		button{margin-top:10px;padding:8px 16px}
	</style>
	<table>
		<thead><tr><th>Plan</th><th>Price</th><th>Support</th></tr></thead>
		<tbody><tr><td>Starter</td><td>$9</td><td>Email</td></tr>
		<tr><td>Team</td><td>$29</td><td>Priority</td></tr></tbody>
	</table>
	<form action="/signup" method="post">
		<label for="name">Name</label><input id="name" name="name" placeholder="Your name">
		<label for="notes">Notes</label><textarea id="notes" name="notes">A short note.</textarea>
		<button type="submit">Create account</button>
	</form>`
	_, layout := h.load("table-form", html)
	raster := h.rasterize("table-form")
	plans := []string{"Plan", "Price", "Support"}
	cells := make([]sdl.Rect, 0, len(plans))
	for _, plan := range plans {
		r := h.textRect(layout, plan)
		h.requireInside("table header "+plan, r)
		cells = append(cells, r)
	}
	if !(cells[0].Y == cells[1].Y && cells[1].Y == cells[2].Y &&
		cells[0].X < cells[1].X && cells[1].X < cells[2].X) {
		t.Fatalf("table header cells are not laid out as one row: %+v", cells)
	}
	for i := 0; i < len(cells); i++ {
		for j := i + 1; j < len(cells); j++ {
			if headlessOverlap(cells[i], cells[j], 2) {
				t.Fatalf("table cells overlap: %+v %+v", cells[i], cells[j])
			}
		}
	}
	if got := h.textRect(layout, "Starter"); got.Y <= cells[0].Y {
		t.Fatalf("table body did not follow its header: header=%+v body=%+v", cells[0], got)
	}
	headerCell := h.boxAround(layout, "Plan")
	h.requireFill(raster, headerCell, sdl.Color{R: 238, G: 238, B: 255, A: 255}, 0.25, "table header cell")
	for _, body := range []string{"Starter", "Team"} {
		cell := h.boxAround(layout, body)
		h.requireFill(raster, cell, sdl.Color{R: 242, G: 247, B: 255, A: 255}, 0.25, "table body cell "+body)
		h.requireTextPixels(raster, layout, body, "table body text")
	}
	controls := 0
	for _, hit := range layout.Links {
		if hit.Control == nil {
			continue
		}
		controls++
		if hit.Box.W <= 0 || hit.Box.H <= 0 {
			t.Fatalf("form control %q has a collapsed hit box: %+v", hit.Control.Kind, hit.Box)
		}
		h.requireInside("form control "+hit.Control.Kind, hit.Box)
	}
	if controls < 3 {
		t.Fatalf("expected name, notes and submit controls, got %d", controls)
	}
	h.requireInside("form button", h.textRect(layout, "Create account"))
	formBox := h.boxAround(layout, "Name")
	h.requireFill(raster, formBox, sdl.Color{R: 246, G: 248, B: 251, A: 255}, 0.25, "form surface")
	h.requireTextPixels(raster, layout, "Name", "form label")
	h.requireTextPixels(raster, layout, "A short note.", "textarea value")
	h.requireTextPixels(raster, layout, "Create account", "submit button")
	h.requireClearRightGutter(raster, "table and form")
}

// TestBrowserHeadlessFixtureInventory keeps the fixture set honest: every
// page shape named by the rendering mission has a test above, and each one
// renders a non-trivial scene rather than an empty document.
// TestBrowserHeadlessLiveSnapshots runs two real pages saved from the network
// through the same path as the fixtures. The snapshots are deliberately kept
// small: their point is that real markup, stylesheets and content shape reach
// the scene without the renderer dropping or clipping the page.
func TestBrowserHeadlessLiveSnapshots(t *testing.T) {
	cases := []struct {
		name, path, baseURL, wantText string
	}{
		{"example.com", "testdata/web_live_example.html", "https://example.com/", "documentation examples"},
		{"w3.org", "testdata/web_live_w3.html", "https://www.w3.org/", "W3C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatalf("read live snapshot %s: %v", tc.path, err)
			}
			h := newHeadlessBrowser(t)
			doc, layout := h.loadAt(tc.name, string(source), tc.baseURL)
			raster := h.rasterize("live-" + strings.ReplaceAll(tc.name, ".", "-"))
			if doc.Title == "" {
				t.Fatalf("%s snapshot has no title", tc.name)
			}
			if len(layout.Rows) < 3 || layout.Height <= 0 {
				t.Fatalf("%s snapshot produced no readable page: rows=%d height=%d", tc.name, len(layout.Rows), layout.Height)
			}
			if !strings.Contains(strings.Join(headlessRunTexts(layout), " "), tc.wantText) {
				t.Fatalf("%s snapshot lost its identity text %q", tc.name, tc.wantText)
			}
			page := h.pageBounds()
			nonPaper, sampled := countPixels(raster, page, func(c color.NRGBA) bool {
				return c.R < 248 || c.G < 248 || c.B < 248
			})
			if nonPaper*100 < sampled {
				t.Fatalf("%s snapshot painted almost no content: %d/%d non-paper pixels", tc.name, nonPaper, sampled)
			}
			h.requireClearRightGutter(raster, tc.name+" snapshot")
			for _, row := range layout.Rows {
				for _, run := range row.Runs {
					if run.Font == nil {
						continue
					}
					w, _, err := run.Font.SizeUTF8(run.Text)
					if err == nil && run.X >= 0 && run.X+int32(w) > page.W+2 {
						t.Fatalf("%s run %q overflows the page: x=%d w=%d page=%d", tc.name, run.Text, run.X, w, page.W)
					}
				}
			}
		})
	}
}

func TestBrowserHeadlessFixtureInventory(t *testing.T) {
	fixtures := map[string]string{
		"flex-cards":     `<div style="display:flex;gap:12px"><div style="flex:1">One</div><div style="flex:1">Two</div></div>`,
		"grid-sidebar":   `<div style="display:grid;grid-template-columns:200px 1fr;gap:12px"><div>Side</div><div>Main</div></div>`,
		"sticky-overlay": `<main><div style="position:sticky">Header</div><div style="position:absolute;top:0;right:0">Overlay</div><p>Page body</p></main>`,
		"hero":           `<div style="background-image:linear-gradient(#111,#333);min-height:180px">Hero</div>`,
		"article":        `<article><h1>Article</h1><p>Text</p><img src="` + headlessPixel + `" width="100" height="60"><ul><li>One</li></ul></article>`,
		"table-form":     `<table><tr><td>A</td><td>B</td></tr></table><form><input name="q"><button>Go</button></form>`,
		"white-space-modes": `<div style="white-space:pre-wrap">Alpha
    indented   words</div><div style="white-space:nowrap">One long unbroken nowrap line</div>`,
		"justified-text": `<p style="text-align:justify;width:320px">Justified copy needs enough words to wrap onto several lines so the gaps can stretch to the right edge.</p>`,
	}
	for name, html := range fixtures {
		t.Run(name, func(t *testing.T) {
			h := newHeadlessBrowser(t)
			doc, layout := h.load(name, html)
			h.rasterize("inventory-" + name)
			if doc == nil || layout == nil || len(doc.Blocks) == 0 || len(layout.Rows) == 0 {
				t.Fatalf("%s did not produce a rendered scene: doc=%+v layout=%+v", name, doc, layout)
			}
			nodes := len(layout.Rows) + len(layout.Rects) + len(layout.Backgrounds) + len(layout.Images) + len(layout.Videos) + len(layout.Floats) + len(layout.Links)
			if nodes == 0 {
				t.Fatalf("%s produced no scene nodes", name)
			}
			for _, row := range layout.Rows {
				for _, run := range row.Runs {
					if run.Font == nil {
						continue
					}
					if run.X < 0 {
						t.Fatalf("%s has a run at negative x: %+v", name, run)
					}
				}
			}
		})
	}
}
