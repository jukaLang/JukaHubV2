package main

// pdfv_view.go — the PDF reader's pages on screen.
//
// A document is opened off the render thread (pdfv_doc.go indexes it), and each
// page is interpreted and its pictures decoded by the same background worker
// when the page is first shown (the next page is fetched ahead). The render
// thread then draws a page into 512-pixel-tall texture tiles at the current
// zoom — text with the bundled Inter at the size the PDF set it, filled and
// stroked shapes, and the page's JPEG and Flate pictures — and every frame after
// that is a few texture copies, so scrolling and panning stay smooth on the
// handheld. At most one tile is drawn per frame; the rest show as blank paper
// for the frame or two they take.
//
// A page with nothing to draw (a scan in a codec the reader cannot decode) or a
// document that only reads as text is shown as text in the reflowing view (X).

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// pdfvFitMode is how a page is sized.
type pdfvFitMode int

const (
	pdfvFitWidth pdfvFitMode = iota
	pdfvFitPage
	pdfvFitCustom
)

const (
	// pdfvTileH is the height of one page tile in pixels.
	pdfvTileH = int32(512)
	// pdfvImageMaxDim bounds a decoded page picture: a 600-dpi scan is far
	// larger than any zoom the handheld can show.
	pdfvImageMaxDim = 1600
	// pdfvPageCache bounds the interpreted pages kept in memory.
	pdfvPageCache = 12
)

// pdfvJob asks the worker for one page of document generation gen.
type pdfvJob struct {
	gen  int
	page int
}

// pdfvDocState is the open PDF, shared between the worker and the render
// thread under mu.
type pdfvDocState struct {
	mu      sync.Mutex
	gen     int
	path    string
	file    *pdfvFile
	refs    []pdfvPageRef
	pages   map[int]*pdfvPageContent
	order   []int
	images  map[int]*image.NRGBA
	pending map[int]bool
	queue   chan pdfvJob
	// loaded is the finished open (or its error) for the render thread to
	// pick up; the reader's pdf.doc is only ever written on that thread.
	loaded *pdfDoc
}

var pdfvDoc pdfvDocState

// pdfvViewState is the render thread's view of the open document.
type pdfvViewState struct {
	textMode bool
	fit      pdfvFitMode
	zoom     float64
	scrollY  float64
	panX     float64
	tiles    []pdfvTile
	imgTex   map[int]*sdl.Texture
	frame    uint64
	chips    []filesChip
	menu     filesMenu
	viewport sdl.Rect
	// pageW/pageH is the current page's size on screen at the resolved zoom.
	pageW, pageH float64
	dragOn       bool
	dragMoved    bool
	dragX, dragY int32
	// textLines caches the wrapped text of the page in the text view.
	textKey   string
	textLines []string
	textSize  int
}

var pdfvView pdfvViewState

// pdfvTile is one drawn slice of a page.
type pdfvTile struct {
	page  int
	zoom  int
	index int
	tex   *sdl.Texture
	used  uint64
}

// pdfvStartLoad opens path in the background and returns at once.
func pdfvStartLoad(path string) {
	pdfvDoc.mu.Lock()
	pdfvDoc.gen++
	gen := pdfvDoc.gen
	if pdfvDoc.queue != nil {
		close(pdfvDoc.queue)
	}
	queue := make(chan pdfvJob, 8)
	pdfvDoc.queue = queue
	pdfvDoc.path = path
	pdfvDoc.file = nil
	pdfvDoc.refs = nil
	pdfvDoc.pages = map[int]*pdfvPageContent{}
	pdfvDoc.order = nil
	pdfvDoc.images = map[int]*image.NRGBA{}
	pdfvDoc.pending = map[int]bool{}
	pdfvDoc.loaded = nil
	pdfvDoc.mu.Unlock()
	go pdfvWorker(gen, path, queue)
}

// pdfvStopLoad forgets the open document (a new one, or an EPUB, replaces it).
func pdfvStopLoad() {
	pdfvDoc.mu.Lock()
	pdfvDoc.gen++
	if pdfvDoc.queue != nil {
		close(pdfvDoc.queue)
		pdfvDoc.queue = nil
	}
	pdfvDoc.file = nil
	pdfvDoc.refs = nil
	pdfvDoc.pages = nil
	pdfvDoc.images = nil
	pdfvDoc.pending = nil
	pdfvDoc.loaded = nil
	pdfvDoc.mu.Unlock()
}

// pdfvWorker opens the document, then interprets pages as they are asked for.
func pdfvWorker(gen int, path string, queue chan pdfvJob) {
	doc := pdfDoc{Path: path, Format: "PDF", Loaded: true}
	file, refs, err := pdfvOpenPath(path)
	if err != nil {
		doc.Error = err.Error()
	} else {
		doc.Title = file.infoString("Title")
		doc.Author = file.infoString("Author")
		if strings.TrimSpace(doc.Title) == "" {
			doc.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		doc.NumPages = len(refs)
		doc.Pages = make([]pdfPage, len(refs))
		for i, ref := range refs {
			doc.Pages[i] = pdfPage{Width: ref.box[2] - ref.box[0], Height: ref.box[3] - ref.box[1]}
		}
		if len(refs) == 0 {
			doc.Error = "This PDF has no pages the reader can find"
		}
	}
	pdfvDoc.mu.Lock()
	if pdfvDoc.gen != gen {
		pdfvDoc.mu.Unlock()
		return
	}
	pdfvDoc.file = file
	pdfvDoc.refs = refs
	pdfvDoc.loaded = &doc
	pdfvDoc.mu.Unlock()
	if err != nil {
		return
	}
	for job := range queue {
		if job.gen != gen || job.page < 0 || job.page >= len(refs) {
			continue
		}
		content := file.loadPage(refs[job.page])
		images := map[int]*image.NRGBA{}
		for _, img := range content.images {
			if pic, err := file.decodeImage(img.num, pdfvImageMaxDim); err == nil {
				images[img.num] = pic
			}
		}
		pdfvDoc.mu.Lock()
		if pdfvDoc.gen != gen {
			pdfvDoc.mu.Unlock()
			return
		}
		pdfvDoc.pages[job.page] = content
		pdfvDoc.order = append(pdfvDoc.order, job.page)
		for num, pic := range images {
			pdfvDoc.images[num] = pic
		}
		delete(pdfvDoc.pending, job.page)
		// Old pages leave the cache first; their pictures go with them when
		// no kept page still draws them.
		for len(pdfvDoc.order) > pdfvPageCache {
			drop := pdfvDoc.order[0]
			pdfvDoc.order = pdfvDoc.order[1:]
			delete(pdfvDoc.pages, drop)
		}
		pdfvDoc.mu.Unlock()
	}
}

// pdfvOpenPath reads and indexes a PDF, with the reasons a user needs.
func pdfvOpenPath(path string) (*pdfvFile, []pdfvPageRef, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("Cannot open %s: %s", filepath.Base(path), filesFriendlyError(err))
	}
	if info.Size() > pdfvMaxFileBytes {
		return nil, nil, fmt.Errorf("%s is %s — too large to open here", filepath.Base(path), humanBytes(info.Size()))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("Cannot read %s: %s", filepath.Base(path), filesFriendlyError(err))
	}
	if !bytes.HasPrefix(bytes.TrimLeft(data[:min(len(data), 1024)], "\x00\r\n\t "), []byte("%PDF")) && !bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF")) {
		return nil, nil, fmt.Errorf("%s is not a PDF file", filepath.Base(path))
	}
	file, err := pdfvOpenBytes(data)
	if err != nil {
		if err == errPDFVLocked {
			return nil, nil, fmt.Errorf("%s is locked with a password, which this reader cannot open", filepath.Base(path))
		}
		return nil, nil, fmt.Errorf("%s could not be read: %v", filepath.Base(path), err)
	}
	return file, file.pages(), nil
}

// pdfvPollLoaded moves a finished open into the reader's state.
func pdfvPollLoaded() {
	pdfvDoc.mu.Lock()
	done := pdfvDoc.loaded
	pdfvDoc.loaded = nil
	pdfvDoc.mu.Unlock()
	if done == nil || !pdf.doc.Loading || done.Path != pdf.doc.Path {
		return
	}
	pdf.doc = *done
	if done.Error == "" && done.Format == "PDF" {
		trophyEvent(tkPdfOpen)
	}
	if pdf.page < 1 {
		pdf.page = 1
	}
}

// pdfvRequest asks the worker for a page unless it is cached or on its way.
func pdfvRequest(page int) {
	pdfvDoc.mu.Lock()
	defer pdfvDoc.mu.Unlock()
	if pdfvDoc.queue == nil || pdfvDoc.pages == nil || page < 0 || page >= len(pdfvDoc.refs) {
		return
	}
	if _, ok := pdfvDoc.pages[page]; ok || pdfvDoc.pending[page] {
		return
	}
	select {
	case pdfvDoc.queue <- pdfvJob{gen: pdfvDoc.gen, page: page}:
		pdfvDoc.pending[page] = true
	default:
	}
}

// pdfvPageContentFor returns a page once the worker has it, and copies its
// text into the reader's page list for the text view.
func pdfvPageContentFor(page int) *pdfvPageContent {
	pdfvDoc.mu.Lock()
	content := pdfvDoc.pages[page]
	pdfvDoc.mu.Unlock()
	if content != nil && page >= 0 && page < len(pdf.doc.Pages) && pdf.doc.Pages[page].Text == "" {
		pdf.doc.Pages[page].Text = content.text
	}
	return content
}

// pdfvImage returns a decoded page picture.
func pdfvImage(num int) *image.NRGBA {
	pdfvDoc.mu.Lock()
	defer pdfvDoc.mu.Unlock()
	return pdfvDoc.images[num]
}

// pdfvIsPagedPDF reports whether the open document is drawn as pages.
func pdfvIsPagedPDF() bool {
	return pdf.doc.Loaded && pdf.doc.Error == "" && pdf.doc.Format == "PDF" && len(pdf.doc.Pages) > 0
}

// --- Images -------------------------------------------------------------------

// decodeImage turns an image XObject into pixels: JPEG through the Go decoder,
// raw samples (Flate and friends) by colour space, an /SMask as alpha.
func (f *pdfvFile) decodeImage(num, maxDim int) (*image.NRGBA, error) {
	ind, ok := f.objs[num]
	if !ok || ind.obj.kind != pdfvDict {
		return nil, fmt.Errorf("missing image")
	}
	d := ind.obj.dict
	data, err := f.streamBytes(num)
	if err != nil {
		return nil, err
	}
	var pic image.Image
	switch f.streamFilter(num) {
	case "DCT":
		pic, err = jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
	case "":
		pic, err = f.rawImage(d, data)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported image codec")
	}
	b := pic.Bounds()
	w, h := imgvFitInside(b.Dx(), b.Dy(), maxDim, maxDim)
	out := imgvScale(pic, w, h)
	if sm := d["SMask"]; sm.kind == pdfvRef {
		if mask, err := f.decodeImage(sm.ref, maxDim); err == nil && mask.Rect.Dx() > 0 {
			pdfvApplyMask(out, mask)
		}
	}
	return out, nil
}

// rawImage builds an image from uncompressed samples.
func (f *pdfvFile) rawImage(d map[string]pdfvObj, data []byte) (image.Image, error) {
	w := int(f.numOf(d["Width"], 0))
	h := int(f.numOf(d["Height"], 0))
	if w <= 0 || h <= 0 || int64(w)*int64(h) > imgvMaxPixels {
		return nil, fmt.Errorf("bad image size")
	}
	bpc := int(f.numOf(d["BitsPerComponent"], 8))
	if mask := f.resolve(d["ImageMask"]); mask.kind == pdfvBool && mask.b {
		bpc = 1
	}
	comps := 1
	var palette []byte
	paletteComps := 0
	cs := f.resolve(d["ColorSpace"])
	csName := cs.name()
	if cs.kind == pdfvArray && len(cs.arr) > 0 {
		csName = f.resolve(cs.arr[0]).name()
		switch csName {
		case "ICCBased":
			if len(cs.arr) > 1 {
				if icc := f.dictOf(cs.arr[1]); icc != nil {
					comps = int(f.numOf(icc["N"], 3))
				}
			}
		case "Indexed", "I":
			if len(cs.arr) >= 4 {
				base := f.resolve(cs.arr[1])
				baseName := base.name()
				if base.kind == pdfvArray && len(base.arr) > 0 {
					baseName = f.resolve(base.arr[0]).name()
					if baseName == "ICCBased" && len(base.arr) > 1 {
						if icc := f.dictOf(base.arr[1]); icc != nil {
							paletteComps = int(f.numOf(icc["N"], 3))
						}
					}
				}
				switch baseName {
				case "DeviceRGB", "RGB", "CalRGB":
					paletteComps = 3
				case "DeviceGray", "G", "CalGray":
					paletteComps = 1
				case "DeviceCMYK", "CMYK":
					paletteComps = 4
				}
				if paletteComps == 0 {
					paletteComps = 3
				}
				lookup := f.resolve(cs.arr[3])
				if lookup.kind == pdfvStr {
					palette = lookup.s
				} else if cs.arr[3].kind == pdfvRef {
					palette, _ = f.streamBytes(cs.arr[3].ref)
				}
			}
		case "CalRGB", "Lab":
			comps = 3
		}
	}
	switch csName {
	case "DeviceRGB", "RGB", "CalRGB":
		comps = 3
	case "DeviceCMYK", "CMYK":
		comps = 4
	case "DeviceGray", "G", "CalGray":
		comps = 1
	}
	invert := false
	if dec := f.arrOf(d["Decode"]); len(dec) >= 2 && f.numOf(dec[0], 0) == 1 && f.numOf(dec[1], 1) == 0 {
		invert = true
	}
	rowBits := w * comps * bpc
	rowBytes := (rowBits + 7) / 8
	if len(data) < rowBytes*h {
		h = len(data) / rowBytes
		if h <= 0 {
			return nil, fmt.Errorf("image data is short")
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	sample := func(row []byte, i int) int {
		switch bpc {
		case 8:
			return int(row[i])
		case 16:
			return int(row[i*2])
		default:
			bit := i * bpc
			v := int(row[bit/8]>>(8-bpc-bit%8)) & (1<<bpc - 1)
			return v * 255 / (1<<bpc - 1)
		}
	}
	for y := 0; y < h; y++ {
		row := data[y*rowBytes : y*rowBytes+rowBytes]
		dst := out.Pix[y*out.Stride:]
		for x := 0; x < w; x++ {
			var r, g, b int
			switch {
			case palette != nil:
				idx := sample(row, x)
				if bpc != 8 {
					idx = idx * (1<<bpc - 1) / 255
				}
				o := idx * paletteComps
				if o+paletteComps <= len(palette) {
					switch paletteComps {
					case 1:
						r, g, b = int(palette[o]), int(palette[o]), int(palette[o])
					case 4:
						c := pdfvCMYK(float64(palette[o])/255, float64(palette[o+1])/255, float64(palette[o+2])/255, float64(palette[o+3])/255)
						r, g, b = int(c[0]), int(c[1]), int(c[2])
					default:
						r, g, b = int(palette[o]), int(palette[o+1]), int(palette[o+2])
					}
				}
			case comps >= 4:
				c := pdfvCMYK(float64(sample(row, x*comps))/255, float64(sample(row, x*comps+1))/255,
					float64(sample(row, x*comps+2))/255, float64(sample(row, x*comps+3))/255)
				r, g, b = int(c[0]), int(c[1]), int(c[2])
			case comps == 3:
				r, g, b = sample(row, x*3), sample(row, x*3+1), sample(row, x*3+2)
			default:
				v := sample(row, x*comps)
				r, g, b = v, v, v
			}
			if invert {
				r, g, b = 255-r, 255-g, 255-b
			}
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = uint8(r), uint8(g), uint8(b), 255
		}
	}
	return out, nil
}

// pdfvApplyMask uses a soft mask's grey as the picture's alpha.
func pdfvApplyMask(pic, mask *image.NRGBA) {
	pw, ph := pic.Rect.Dx(), pic.Rect.Dy()
	mw, mh := mask.Rect.Dx(), mask.Rect.Dy()
	if pw == 0 || ph == 0 || mw == 0 || mh == 0 {
		return
	}
	for y := 0; y < ph; y++ {
		my := y * mh / ph
		for x := 0; x < pw; x++ {
			mx := x * mw / pw
			pic.Pix[y*pic.Stride+x*4+3] = mask.Pix[my*mask.Stride+mx*4]
		}
	}
}

// --- Geometry -------------------------------------------------------------------

// pdfvPageSize is the page's size in points as displayed (a rotated page swaps
// its sides).
func pdfvPageSize(content *pdfvPageContent) (float64, float64) {
	w := content.box[2] - content.box[0]
	h := content.box[3] - content.box[1]
	if content.rotate == 90 || content.rotate == 270 {
		return h, w
	}
	return w, h
}

// pdfvMap turns a page point into tile pixels at zoom z.
func pdfvMap(content *pdfvPageContent, x, y, z float64) (float64, float64) {
	b := content.box
	switch content.rotate {
	case 90:
		return (y - b[1]) * z, (x - b[0]) * z
	case 180:
		return (b[2] - x) * z, (y - b[1]) * z
	case 270:
		return (b[3] - y) * z, (b[2] - x) * z
	}
	return (x - b[0]) * z, (b[3] - y) * z
}

// pdfvMaxPagePixels bounds the page's drawn width: one tile is that wide.
func pdfvMaxPagePixels() float64 {
	if IsTrimuiDevice() {
		return 2048
	}
	return 4096
}

// pdfvResolveZoom is the pixels per point the current fit mode asks for.
func pdfvResolveZoom(content *pdfvPageContent, view sdl.Rect) float64 {
	pw, ph := pdfvPageSize(content)
	if pw <= 0 || ph <= 0 {
		return 1
	}
	fitW := float64(view.W-24) / pw
	fitP := math.Min(fitW, float64(view.H-16)/ph)
	z := fitW
	switch pdfvView.fit {
	case pdfvFitPage:
		z = fitP
	case pdfvFitCustom:
		z = pdfvView.zoom
	}
	lo := fitP * 0.5
	hi := math.Min(fitW*4, pdfvMaxPagePixels()/pw)
	if z < lo {
		z = lo
	}
	if z > hi {
		z = hi
	}
	return z
}

// --- Tiles ----------------------------------------------------------------------

// pdfvReleaseTextures frees every page tile and picture texture: a new
// document, a zoom change, or leaving the reader.
func pdfvReleaseTextures() {
	for _, t := range pdfvView.tiles {
		if t.tex != nil {
			t.tex.Destroy()
		}
	}
	pdfvView.tiles = nil
	for num, tex := range pdfvView.imgTex {
		if tex != nil {
			tex.Destroy()
		}
		delete(pdfvView.imgTex, num)
	}
}

// pdfvMaxTiles bounds the tiles kept: about two screens of pages.
func pdfvMaxTiles() int {
	if IsTrimuiDevice() {
		return 6
	}
	return 10
}

// pdfvTileFor returns a page tile, drawing it when allowed (one per frame).
func pdfvTileFor(renderer *sdl.Renderer, config *Config, content *pdfvPageContent, page, index int, zoom float64, mayDraw *bool) *sdl.Texture {
	key := int(zoom * 1000)
	for i := range pdfvView.tiles {
		t := &pdfvView.tiles[i]
		if t.page == page && t.zoom == key && t.index == index {
			t.used = pdfvView.frame
			return t.tex
		}
	}
	if !*mayDraw {
		return nil
	}
	*mayDraw = false
	tex := pdfvDrawTile(renderer, config, content, index, zoom)
	if tex == nil {
		return nil
	}
	if len(pdfvView.tiles) >= pdfvMaxTiles() {
		oldest := 0
		for i := range pdfvView.tiles {
			if pdfvView.tiles[i].used < pdfvView.tiles[oldest].used {
				oldest = i
			}
		}
		pdfvView.tiles[oldest].tex.Destroy()
		pdfvView.tiles = append(pdfvView.tiles[:oldest], pdfvView.tiles[oldest+1:]...)
	}
	pdfvView.tiles = append(pdfvView.tiles, pdfvTile{page: page, zoom: key, index: index, tex: tex, used: pdfvView.frame})
	return tex
}

// pdfvDrawTile draws one 512-pixel slice of a page into a texture.
func pdfvDrawTile(renderer *sdl.Renderer, config *Config, content *pdfvPageContent, index int, zoom float64) *sdl.Texture {
	if renderer == nil || !renderer.RenderTargetSupported() {
		return nil
	}
	pw, ph := pdfvPageSize(content)
	w := int32(math.Ceil(pw * zoom))
	total := int32(math.Ceil(ph * zoom))
	top := int32(index) * pdfvTileH
	h := total - top
	if h > pdfvTileH {
		h = pdfvTileH
	}
	if w <= 0 || h <= 0 {
		return nil
	}
	tex, err := renderer.CreateTexture(sdl.PIXELFORMAT_ARGB8888, sdl.TEXTUREACCESS_TARGET, w, h)
	if err != nil || tex == nil {
		return nil
	}
	prev := renderer.GetRenderTarget()
	if err := renderer.SetRenderTarget(tex); err != nil {
		tex.Destroy()
		return nil
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = renderer.SetDrawColor(255, 255, 255, 255)
	_ = renderer.Clear()
	offY := float64(top)
	tileBottom := float64(h)
	for _, item := range content.items {
		switch item.kind {
		case pdfvItemFill:
			pdfvFillPaths(renderer, content, item, zoom, offY, tileBottom)
		case pdfvItemStroke:
			pdfvStrokePaths(renderer, content, item, zoom, offY)
		case pdfvItemImage:
			pdfvDrawImage(renderer, content, item, zoom, offY, tileBottom)
		case pdfvItemText:
			pdfvDrawText(renderer, config, content, item, zoom, offY, tileBottom)
		}
	}
	_ = renderer.SetRenderTarget(prev)
	return tex
}

// pdfvPathBounds is a path item's bounding box in tile pixels.
func pdfvPathBounds(content *pdfvPageContent, paths [][]pdfvPt, zoom, offY float64) (float64, float64, float64, float64) {
	x0, y0 := math.Inf(1), math.Inf(1)
	x1, y1 := math.Inf(-1), math.Inf(-1)
	for _, path := range paths {
		for _, p := range path {
			x, y := pdfvMap(content, p.x, p.y, zoom)
			y -= offY
			x0, y0 = math.Min(x0, x), math.Min(y0, y)
			x1, y1 = math.Max(x1, x), math.Max(y1, y)
		}
	}
	return x0, y0, x1, y1
}

// pdfvFillPaths fills a path: an axis-aligned rectangle as one rect, anything
// else by scanlines (even-odd).
func pdfvFillPaths(renderer *sdl.Renderer, content *pdfvPageContent, item pdfvItem, zoom, offY, tileBottom float64) {
	x0, y0, x1, y1 := pdfvPathBounds(content, item.paths, zoom, offY)
	if y1 < 0 || y0 > tileBottom || x1-x0 < 0.5 && y1-y0 < 0.5 {
		return
	}
	_ = renderer.SetDrawColor(item.color[0], item.color[1], item.color[2], 255)
	if len(item.paths) == 1 && pdfvIsRect(content, item.paths[0]) {
		_ = renderer.FillRect(&sdl.Rect{X: int32(math.Floor(x0)), Y: int32(math.Floor(y0)),
			W: int32(math.Max(1, math.Ceil(x1-x0))), H: int32(math.Max(1, math.Ceil(y1-y0)))})
		return
	}
	type edge struct{ ax, ay, bx, by float64 }
	var edges []edge
	for _, path := range item.paths {
		for i := 1; i < len(path) && len(edges) < 4000; i++ {
			ax, ay := pdfvMap(content, path[i-1].x, path[i-1].y, zoom)
			bx, by := pdfvMap(content, path[i].x, path[i].y, zoom)
			edges = append(edges, edge{ax, ay - offY, bx, by - offY})
		}
		if n := len(path); n > 2 {
			ax, ay := pdfvMap(content, path[n-1].x, path[n-1].y, zoom)
			bx, by := pdfvMap(content, path[0].x, path[0].y, zoom)
			edges = append(edges, edge{ax, ay - offY, bx, by - offY})
		}
	}
	startY := int(math.Max(0, math.Floor(y0)))
	endY := int(math.Min(tileBottom, math.Ceil(y1)))
	xs := make([]float64, 0, 16)
	for y := startY; y < endY; y++ {
		sy := float64(y) + 0.5
		xs = xs[:0]
		for _, e := range edges {
			if (e.ay <= sy && e.by > sy) || (e.by <= sy && e.ay > sy) {
				xs = append(xs, e.ax+(sy-e.ay)*(e.bx-e.ax)/(e.by-e.ay))
			}
		}
		if len(xs) < 2 {
			continue
		}
		for i := 1; i < len(xs); i++ {
			for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
				xs[j], xs[j-1] = xs[j-1], xs[j]
			}
		}
		for i := 0; i+1 < len(xs); i += 2 {
			a, b := int32(math.Round(xs[i])), int32(math.Round(xs[i+1]))
			if b > a {
				_ = renderer.FillRect(&sdl.Rect{X: a, Y: int32(y), W: b - a, H: 1})
			}
		}
	}
}

// pdfvIsRect reports whether a subpath is an axis-aligned rectangle on screen.
func pdfvIsRect(content *pdfvPageContent, path []pdfvPt) bool {
	if len(path) < 4 || len(path) > 5 {
		return false
	}
	for i := 1; i < len(path); i++ {
		ax, ay := pdfvMap(content, path[i-1].x, path[i-1].y, 1)
		bx, by := pdfvMap(content, path[i].x, path[i].y, 1)
		if math.Abs(ax-bx) > 0.01 && math.Abs(ay-by) > 0.01 {
			return false
		}
	}
	return true
}

// pdfvStrokePaths draws a stroked path: thin lines as lines, thick horizontal
// and vertical ones as rectangles.
func pdfvStrokePaths(renderer *sdl.Renderer, content *pdfvPageContent, item pdfvItem, zoom, offY float64) {
	_ = renderer.SetDrawColor(item.color[0], item.color[1], item.color[2], 255)
	width := item.width * zoom
	for _, path := range item.paths {
		for i := 1; i < len(path); i++ {
			ax, ay := pdfvMap(content, path[i-1].x, path[i-1].y, zoom)
			bx, by := pdfvMap(content, path[i].x, path[i].y, zoom)
			ay -= offY
			by -= offY
			if math.Max(ay, by) < -width || math.Min(ay, by) > float64(pdfvTileH)+width {
				continue
			}
			switch {
			case width >= 1.5 && math.Abs(ay-by) < 0.01:
				_ = renderer.FillRect(&sdl.Rect{X: int32(math.Min(ax, bx)), Y: int32(ay - width/2),
					W: int32(math.Max(1, math.Abs(bx-ax))), H: int32(math.Max(1, width))})
			case width >= 1.5 && math.Abs(ax-bx) < 0.01:
				_ = renderer.FillRect(&sdl.Rect{X: int32(ax - width/2), Y: int32(math.Min(ay, by)),
					W: int32(math.Max(1, width)), H: int32(math.Max(1, math.Abs(by-ay)))})
			default:
				_ = renderer.DrawLine(int32(ax), int32(ay), int32(bx), int32(by))
			}
		}
	}
}

// pdfvDrawImage copies a page picture into its box (a grey frame stands in for
// a picture in a format the reader cannot decode).
func pdfvDrawImage(renderer *sdl.Renderer, content *pdfvPageContent, item pdfvItem, zoom, offY, tileBottom float64) {
	ax, ay := pdfvMap(content, item.x, item.y, zoom)
	bx, by := pdfvMap(content, item.x+item.w, item.y+item.h, zoom)
	x0, x1 := math.Min(ax, bx), math.Max(ax, bx)
	y0, y1 := math.Min(ay, by)-offY, math.Max(ay, by)-offY
	if y1 < 0 || y0 > tileBottom || x1-x0 < 1 || y1-y0 < 1 {
		return
	}
	dst := sdl.Rect{X: int32(x0), Y: int32(y0), W: int32(math.Ceil(x1 - x0)), H: int32(math.Ceil(y1 - y0))}
	if item.image < 0 || item.image >= len(content.images) {
		return
	}
	num := content.images[item.image].num
	tex := pdfvView.imgTex[num]
	if tex == nil {
		if pic := pdfvImage(num); pic != nil {
			tex = imgvTexture(renderer, pic)
			if tex != nil {
				if pdfvView.imgTex == nil {
					pdfvView.imgTex = map[int]*sdl.Texture{}
				}
				pdfvView.imgTex[num] = tex
			}
		}
	}
	if tex == nil {
		_ = renderer.SetDrawColor(225, 228, 234, 255)
		_ = renderer.FillRect(&dst)
		_ = renderer.SetDrawColor(190, 195, 205, 255)
		_ = renderer.DrawRect(&dst)
		return
	}
	_ = renderer.Copy(tex, nil, &dst)
}

// pdfvFontPixels quantises a text size so a page uses a handful of font sizes
// rather than one open face per fraction of a point.
func pdfvFontPixels(px float64) int {
	n := int(math.Round(px))
	switch {
	case n < 5:
		return 0
	case n <= 14:
		return n
	case n <= 28:
		return n / 2 * 2
	case n <= 56:
		return n / 4 * 4
	case n > 160:
		return 160
	}
	return n / 8 * 8
}

// pdfvDrawText renders one run with Inter at the run's size, squeezed back to
// the run's own width when Inter sets it wider (so a line never runs into the
// next column).
func pdfvDrawText(renderer *sdl.Renderer, config *Config, content *pdfvPageContent, item pdfvItem, zoom, offY, tileBottom float64) {
	px := pdfvFontPixels(item.size * zoom)
	if px == 0 {
		return
	}
	sx, sy := pdfvMap(content, item.x, item.y, zoom)
	sy -= offY
	if sy < -float64(px)*2 || sy > tileBottom+float64(px) {
		return
	}
	font := loadRoleFont(config, "small", px)
	if font == nil {
		return
	}
	text := sanitizeCached(font, item.text)
	if text == "" {
		return
	}
	surface, err := font.RenderUTF8Blended(text, sdl.Color{R: item.color[0], G: item.color[1], B: item.color[2], A: 255})
	if err != nil || surface == nil {
		return
	}
	defer surface.Free()
	tex, err := renderer.CreateTextureFromSurface(surface)
	if err != nil || tex == nil {
		return
	}
	defer tex.Destroy()
	dst := sdl.Rect{X: int32(sx), Y: int32(sy) - int32(font.Ascent()), W: surface.W, H: surface.H}
	if want := item.w * zoom; want > 4 && float64(dst.W) > want*1.06 {
		dst.W = int32(want * 1.03)
	}
	_ = renderer.Copy(tex, nil, &dst)
}

// --- Render ---------------------------------------------------------------------

// pdfvRenderPages draws the current page in the viewport: the paper with a
// soft edge, its tiles, and the paper's position.
func pdfvRenderPages(renderer *sdl.Renderer, config *Config, view sdl.Rect) {
	pdfvView.frame++
	page := pdf.page - 1
	content := pdfvPageContentFor(page)
	pdfvRequest(page)
	pdfvRequest(page + 1)
	if content == nil {
		UIEmptyState(renderer, config, view, "@pdf", "Opening page "+filesNumberString(int64(pdf.page))+"…", pdfDocumentTitle(pdf.doc))
		return
	}
	if len(content.items) == 0 {
		// Nothing drawable: say so, and offer the text (there may be some in
		// a layer the page does not paint).
		title, body := "This page has nothing the reader can draw", "It may be a scan in a format it cannot decode."
		if strings.TrimSpace(content.text) != "" {
			body = "X shows its text."
		}
		UIEmptyState(renderer, config, view, "@pdf", title, body)
		return
	}
	zoom := pdfvResolveZoom(content, view)
	pw, ph := pdfvPageSize(content)
	pageW, pageH := pw*zoom, ph*zoom
	pdfvView.pageW, pdfvView.pageH = pageW, pageH
	pdfvView.zoom = zoom
	pdfvClampScroll(view)

	x := float64(view.X) + (float64(view.W)-pageW)/2
	if pageW > float64(view.W) {
		x = float64(view.X) - pdfvView.panX
	}
	y := float64(view.Y) + 8 - pdfvView.scrollY
	if pageH < float64(view.H)-16 {
		y = float64(view.Y) + (float64(view.H)-pageH)/2
	}
	paper := sdl.Rect{X: int32(x), Y: int32(y), W: int32(pageW), H: int32(pageH)}
	// The visible tiles are fetched (and at most one drawn) before the clip is
	// set: drawing a tile switches the render target, and the copies below
	// should not depend on how a driver restores the clip afterwards.
	type placed struct {
		tex *sdl.Texture
		dst sdl.Rect
	}
	var visible [8]placed
	count := 0
	mayDraw := true
	tiles := int((int32(pageH) + pdfvTileH - 1) / pdfvTileH)
	for i := 0; i < tiles && count < len(visible); i++ {
		ty := paper.Y + int32(i)*pdfvTileH
		if ty+pdfvTileH < view.Y || ty > view.Y+view.H {
			continue
		}
		if tex := pdfvTileFor(renderer, config, content, page, i, zoom, &mayDraw); tex != nil {
			th := pdfvTileH
			if remaining := int32(pageH) - int32(i)*pdfvTileH; remaining < th {
				th = remaining
			}
			visible[count] = placed{tex: tex, dst: sdl.Rect{X: paper.X, Y: ty, W: int32(math.Ceil(pageW)), H: th}}
			count++
		}
	}
	renderWithClip(renderer, view.X, view.Y, view.W, view.H, func(r *sdl.Renderer) {
		fillRoundedRect(r, paper.X-3, paper.Y-3, paper.W+6, paper.H+6, 6, glassA(30))
		_ = r.SetDrawColor(255, 255, 255, 255)
		_ = r.FillRect(&paper)
		for i := 0; i < count; i++ {
			dst := visible[i].dst
			_ = r.Copy(visible[i].tex, nil, &dst)
		}
	})
	// Where the view is on this page: a slim bar on the right.
	if pageH > float64(view.H) {
		thumb := float64(view.H) / pageH
		pos := pdfvView.scrollY / math.Max(1, pageH-float64(view.H)+16)
		drawScrollbar(renderer, view.X+view.W-6, view.Y+6, 3, view.H-12, thumb, pos)
	}
}

// pdfvClampScroll keeps the view on the page.
func pdfvClampScroll(view sdl.Rect) {
	maxY := pdfvView.pageH - float64(view.H) + 16
	if maxY < 0 {
		maxY = 0
	}
	if pdfvView.scrollY > maxY {
		pdfvView.scrollY = maxY
	}
	if pdfvView.scrollY < 0 {
		pdfvView.scrollY = 0
	}
	maxX := pdfvView.pageW - float64(view.W)
	if maxX < 0 {
		maxX = 0
	}
	if pdfvView.panX > maxX {
		pdfvView.panX = maxX
	}
	if pdfvView.panX < 0 {
		pdfvView.panX = 0
	}
}

// pdfvScroll moves down (dy > 0) or up the page, turning the page at either
// end so reading flows from one page into the next.
func pdfvScroll(dy float64) {
	view := pdfvView.viewport
	maxY := pdfvView.pageH - float64(view.H) + 16
	if maxY < 0 {
		maxY = 0
	}
	switch {
	case dy > 0 && pdfvView.scrollY >= maxY-0.5:
		if pdfNextPage() {
			pdfvView.scrollY = 0
		}
	case dy < 0 && pdfvView.scrollY <= 0.5:
		if pdfPrevPage() {
			pdfvView.scrollY = 1 << 30
		}
	default:
		pdfvView.scrollY += dy
		pdfvClampScroll(view)
	}
}

// pdfvSide answers Left/Right: pan a page wider than the screen, else turn.
func pdfvSide(dir int) {
	if pdfvView.textMode || pdfvView.pageW <= float64(pdfvView.viewport.W) {
		if dir < 0 {
			if pdfPrevPage() {
				pdfvView.scrollY = 0
			}
		} else if pdfNextPage() {
			pdfvView.scrollY = 0
		}
		return
	}
	pdfvView.panX += float64(dir) * float64(pdfvView.viewport.W) / 4
	pdfvClampScroll(pdfvView.viewport)
	PlayNavSound()
}

// pdfvZoomStep zooms in or out one step from the current size.
func pdfvZoomStep(steps int) {
	if pdfvView.textMode || !pdfvIsPagedPDF() {
		pdfvStepTextSize(steps)
		return
	}
	cur := pdfvView.zoom
	if cur <= 0 {
		cur = 1
	}
	pdfvView.zoom = cur * math.Pow(1.25, float64(steps))
	pdfvView.fit = pdfvFitCustom
	pdfvView.panX = pdfvView.panX * 1.25
	PlayNavSound()
}

// pdfvCycleFit steps Fit width → Fit page.
func pdfvCycleFit() {
	if pdfvView.fit == pdfvFitWidth {
		pdfvView.fit = pdfvFitPage
	} else {
		pdfvView.fit = pdfvFitWidth
	}
	pdfvView.scrollY, pdfvView.panX = 0, 0
	PlayNavSound()
	showToast(pdfvFitLabel(), ToastInfo())
}

func pdfvFitLabel() string {
	switch pdfvView.fit {
	case pdfvFitPage:
		return "Fit page"
	case pdfvFitCustom:
		return fmt.Sprintf("%.0f%%", pdfvView.zoom*100/(96.0/72.0))
	}
	return "Fit width"
}

// pdfvToggleText switches between the drawn page and its text.
func pdfvToggleText() {
	if pdf.doc.Format != "PDF" {
		return
	}
	pdfvView.textMode = !pdfvView.textMode
	pdf.scrollY = 0
	PlayNavSound()
	if pdfvView.textMode {
		showToast("Text view — the page's words, reflowed", ToastInfo())
	} else {
		showToast("Page view", ToastInfo())
	}
}

// pdfvTextSizes are the text view's sizes in design px.
var pdfvTextSizes = []int{18, 20, 22, 24, 28, 32}

func pdfvStepTextSize(steps int) {
	if pdfvView.textSize == 0 {
		pdfvView.textSize = 22
	}
	idx := 2
	for i, s := range pdfvTextSizes {
		if s == pdfvView.textSize {
			idx = i
		}
	}
	idx = clampIndex(idx+steps, len(pdfvTextSizes))
	pdfvView.textSize = pdfvTextSizes[idx]
	pdfvView.textKey = ""
	PlayNavSound()
}

// pdfvSceneActive reports whether the reader is the scene on screen.
func pdfvSceneActive(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == pdfvSceneName
}

// pdfvWheel scrolls the page (or zooms with Ctrl held).
func pdfvWheel(notches int32) bool {
	if notches == 0 || pdfvView.menu.open {
		return true
	}
	if uint32(sdl.GetModState())&uint32(sdl.KMOD_CTRL) != 0 {
		pdfvZoomStep(int(notches))
		return true
	}
	if pdfvView.textMode || !pdfvIsPagedPDF() {
		pdfScrollBy(float64(-notches) * 60)
		return true
	}
	pdfvScroll(float64(-notches) * float64(pdfvView.viewport.H) / 6)
	return true
}

// pdfvMouseDown answers a press on the reader: chips act, the page drags.
func pdfvMouseDown(mx, my int32, config *Config) bool {
	if !pdfvSceneActive(config) {
		return false
	}
	if pdfvView.menu.open {
		pdfvMenuApply(filesMenuMouse(&pdfvView.menu, mx, my))
		return true
	}
	if id := filesChipAt(pdfvView.chips, mx, my); id != "" {
		pdfvChip(id, config)
		return true
	}
	if pointInRect(mx, my, pdfvView.viewport) {
		pdfvView.dragOn, pdfvView.dragMoved = true, false
		pdfvView.dragX, pdfvView.dragY = mx, my
		return true
	}
	return false
}

func pdfvMouseMotion(mx, my int32, leftHeld bool) {
	if !pdfvView.dragOn {
		return
	}
	if !leftHeld {
		pdfvView.dragOn = false
		return
	}
	dx, dy := mx-pdfvView.dragX, my-pdfvView.dragY
	if !pdfvView.dragMoved && dx*dx+dy*dy < 36 {
		return
	}
	pdfvView.dragMoved = true
	pdfvView.dragX, pdfvView.dragY = mx, my
	if pdfvView.textMode || !pdfvIsPagedPDF() {
		pdfScrollBy(float64(-dy))
		return
	}
	pdfvView.scrollY -= float64(dy)
	pdfvView.panX -= float64(dx)
	pdfvClampScroll(pdfvView.viewport)
}

func pdfvMouseUp() {
	pdfvView.dragOn, pdfvView.dragMoved = false, false
}

// pdfvChip runs a toolbar chip.
func pdfvChip(id string, config *Config) {
	switch id {
	case "prev":
		pdfPrevPage()
		pdfvView.scrollY = 0
	case "next":
		pdfNextPage()
		pdfvView.scrollY = 0
	case "zoomout":
		pdfvZoomStep(-1)
	case "zoomin":
		pdfvZoomStep(1)
	case "fit":
		pdfvCycleFit()
	case "text":
		pdfvToggleText()
	case "options":
		pdfvOpenMenu()
	case "system":
		filesOpenWithSystem(pdf.doc.Path)
	case "back":
		goBackScene(config)
	}
}

// --- Options sheet ----------------------------------------------------------------

func pdfvOpenMenu() {
	pdfvView.menu = filesMenu{open: true, title: "Reader"}
	pdfvMenuRows()
	PlayActivateSound()
}

func pdfvMenuRows() {
	paged := pdfvIsPagedPDF()
	view := "Show as text"
	if pdfvView.textMode {
		view = "Show the page"
	}
	rows := []filesMenuRow{
		{ID: "first", Label: "First page", Enabled: pdfPageCount() > 1},
		{ID: "back10", Label: "Back 10 pages", Enabled: pdf.page > 1},
		{ID: "fwd10", Label: "Forward 10 pages", Enabled: pdf.page < pdfPageCount()},
		{ID: "last", Label: "Last page", Enabled: pdfPageCount() > 1},
		{ID: "fit", Label: "Page size", Value: pdfvFitLabel(), Enabled: paged && !pdfvView.textMode},
		{ID: "zoom", Label: "Zoom", Value: pdfvZoomLabel(), Adjust: true, Enabled: true},
		{ID: "text", Label: view, Enabled: pdf.doc.Format == "PDF" && pdf.doc.Error == ""},
	}
	if filesSystemOpenAvailable() && pdf.doc.Path != "" {
		rows = append(rows, filesMenuRow{ID: "system", Label: "Open with Windows", Enabled: true})
	}
	pdfvView.menu.rows = rows
}

func pdfvZoomLabel() string {
	if pdfvView.textMode || !pdfvIsPagedPDF() {
		size := pdfvView.textSize
		if size == 0 {
			size = 22
		}
		return filesNumberString(int64(size)) + " px"
	}
	return pdfvFitLabel()
}

func pdfvMenuApply(in filesMenuInput) {
	switch in.run {
	case "first":
		pdfGoToPage(1)
	case "back10":
		pdfGoToPage(pdf.page - 10)
	case "fwd10":
		pdfGoToPage(pdf.page + 10)
	case "last":
		pdfGoToPage(pdfPageCount())
	case "fit":
		pdfvCycleFit()
	case "zoom":
		step := in.adjust
		if step == 0 {
			step = 1
		}
		pdfvZoomStep(step)
	case "text":
		pdfvView.menu.open = false
		pdfvToggleText()
	case "system":
		pdfvView.menu.open = false
		filesOpenWithSystem(pdf.doc.Path)
	}
	if in.run == "first" || in.run == "back10" || in.run == "fwd10" || in.run == "last" {
		pdfvView.scrollY = 0
	}
	if pdfvView.menu.open {
		pdfvMenuRows()
	}
}

// pdfvTextWrapped is the text view's lines for a page, cached per page, width
// and size: wrapping measures every word.
func pdfvTextWrapped(font *ttf.Font, page int, text string, width int32) []string {
	key := fmt.Sprintf("%s|%d|%d|%p", pdf.doc.Path, page, width, font)
	if key == pdfvView.textKey {
		return pdfvView.textLines
	}
	pdfvView.textKey = key
	pdfvView.textLines = pdfWrapLines(font, text, width)
	return pdfvView.textLines
}
