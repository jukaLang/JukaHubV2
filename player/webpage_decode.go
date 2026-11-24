package main

// webpage_decode.go — pictures decoded off the render thread, at the size they
// are drawn.
//
// drawWebImage used to decode a picture the first frame it was drawn: on the
// render thread, at its full size, through SDL_image straight into a texture.
// On a handheld a hero photo cost on the order of a hundred milliseconds of
// decoding plus a 12 MB upload in the middle of a scroll, a 4000×3000 camera
// picture kept 48 MB of texture for a 600-px box (the 64 MB picture cache then
// evicted the rest of the page and decoded it again on the way back), and a
// picture longer than the GPU's texture limit never drew at all.
//
// On a TrimUI handheld (webTuneNow: DecodeWorkers > 0) a picture that is not
// in the texture cache yet is handed to a small pool of decode threads
// instead, and the frame draws nothing in its box. A thread decodes it with
// SDL_image, refuses a canvas larger than the tune's MaxPixels, and scales it
// down with an area filter to the size its box needs on this screen — the
// picture covering the box, aspect kept, never enlarged, never past the GPU's
// limit — into RGBA, the texture format GLES takes without converting. The
// render thread turns finished pictures into textures at the start of the next
// page draw, a few megabytes per frame (webDecodeUpload).
//
// Pictures in view go first. The ones within the tune's window below and above
// the view (two screens ahead on the Smart Pro S, one on the A133P models) are
// queued behind them, so they are usually ready by the time they scroll in. A
// job nobody has asked for in the last second and a half — a page left, a
// picture flicked past — is dropped unstarted. A texture smaller than a later
// draw needs (the page zoomed in) is decoded again at the larger size, and the
// smaller one is drawn until then.
//
// Everything else — a PC, the Linux test harness — keeps the synchronous path
// in drawWebImage, with only the pixel limit added.

import (
	"math"
	"runtime"
	"sync"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// webDecodeStaleFrames is how many page draws a queued job may go unasked
// before a decode thread drops it unstarted (about 1.5 s at 30 fps).
const webDecodeStaleFrames = 45

// webDecodeQueueMax bounds each queue; the oldest ahead job gives way.
const webDecodeQueueMax = 96

// webDecodeMinShrink is the smallest reduction worth filtering: a picture
// within 10 % of its box keeps its own pixels and the GPU scales it.
const webDecodeMinShrink = 0.9

// webDecodeJob is one picture to decode.
type webDecodeJob struct {
	key        string // texture-cache key (webImageCacheKey)
	data       []byte // the encoded file, shared with the document
	wantW      int32  // the texture size the draw needs; 0 keeps the natural size
	wantH      int32
	natW, natH int32 // the natural size from the header, 0 when unknown
	gen        uint64
}

// webDecodeResult is one finished picture, waiting for the render thread.
type webDecodeResult struct {
	key     string
	surface *sdl.Surface // ABGR8888; nil when the decode failed
	w, h    int32
	gen     uint64
}

// webDecodeEntry is what the pool knows about one key.
type webDecodeEntry struct {
	wantW, wantH int32  // the size queued or decoding
	seen         uint64 // the last page draw that asked for it
	urgent       bool   // asked for by a draw, not only by the look-ahead
	busy         bool   // a thread is decoding it
}

// webDecode is the pool. mu guards every field; cond wakes the threads.
var webDecode struct {
	mu       sync.Mutex
	cond     *sync.Cond
	started  int
	gen      uint64
	frame    uint64
	urgent   []webDecodeJob
	ahead    []webDecodeJob
	entries  map[string]*webDecodeEntry
	done     []webDecodeResult
	failed   map[string]bool
	made     map[string][2]int32 // texture size made per key
	inflight int64
	// limit is the renderer's texture limit as last read (render thread).
	limit int32
	// The last look-ahead scan: the layout, its scroll and the page draw it
	// ran in (webDecodeAhead). Cleared with the page, so a released layout is
	// not kept alive here.
	aheadLayout *webLayout
	aheadOffset int32
	aheadFrame  uint64
}

// webDecodeAsync reports whether pictures decode on the pool on this machine.
func webDecodeAsync() bool { return webTuneNow().DecodeWorkers > 0 }

// webDecodeStart starts the pool's threads once. Callers hold webDecode.mu.
func webDecodeStartLocked(tune webTune) {
	if webDecode.cond == nil {
		webDecode.cond = sync.NewCond(&webDecode.mu)
	}
	if webDecode.entries == nil {
		webDecode.entries = make(map[string]*webDecodeEntry, 64)
		webDecode.failed = make(map[string]bool, 16)
		webDecode.made = make(map[string][2]int32, 64)
	}
	for webDecode.started < tune.DecodeWorkers {
		webDecode.started++
		go webDecodeWorker(tune)
	}
}

// webDecodeNeed is the texture size a box needs from a picture of natural size
// natW×natH, never larger than the picture: for the default fit and cover the
// picture scaled, aspect kept, until it covers the box; for contain until it
// fits inside it; for fill each side on its own, since the draw stretches it
// anyway. object-fit none and scale-down draw the natural pixels, so they keep
// the natural size, and an unknown natural size is kept too. A side within
// 10 % of the natural one keeps it. limit caps the longer side (the GPU's
// texture limit) whatever the box. 0, 0 means the natural size.
func webDecodeNeed(natW, natH, boxW, boxH int32, fit int, limit int32) (int32, int32) {
	if natW <= 0 || natH <= 0 {
		return 0, 0
	}
	sx, sy := 1.0, 1.0
	if fit != webObjectFitNone && fit != webObjectFitScaleDown && boxW > 0 && boxH > 0 {
		bx, by := float64(boxW)/float64(natW), float64(boxH)/float64(natH)
		switch fit {
		case webObjectFitFill:
			sx, sy = bx, by
		case webObjectFitContain:
			sx = math.Min(bx, by)
			sy = sx
		default:
			sx = math.Max(bx, by)
			sy = sx
		}
		if sx > webDecodeMinShrink {
			sx = 1
		}
		if sy > webDecodeMinShrink {
			sy = 1
		}
	}
	if limit > 0 {
		if s := float64(limit) / float64(natW); s < sx {
			sx = s
		}
		if s := float64(limit) / float64(natH); s < sy {
			sy = s
		}
		if fit != webObjectFitFill {
			// Keep the aspect for every fit that does not stretch.
			sx = math.Min(sx, sy)
			sy = sx
		}
	}
	if sx >= 1 && sy >= 1 {
		return 0, 0
	}
	w := int32(math.Ceil(float64(natW) * math.Min(sx, 1)))
	h := int32(math.Ceil(float64(natH) * math.Min(sy, 1)))
	return max(w, 1), max(h, 1)
}

// webDecodeCovers reports whether a texture made at have (0,0 = natural) is
// good enough for a draw that needs want: within 20 % on both sides.
func webDecodeCovers(have, want [2]int32) bool {
	if have[0] == 0 || have[1] == 0 {
		return true
	}
	if want[0] == 0 || want[1] == 0 {
		return false
	}
	return float64(have[0]) >= float64(want[0])*0.8 && float64(have[1]) >= float64(want[1])*0.8
}

// webDecodeState is what a draw learns from webDecodeRequest.
type webDecodeState int

const (
	webDecodePending webDecodeState = iota // queued or decoding: draw nothing yet
	webDecodeFailed                        // will not decode: draw the placeholder
)

// webDecodeRequest asks the pool for key's picture at the size a draw of
// box (fit) needs. urgent marks a picture in view. Render thread.
func webDecodeRequest(doc *webDocument, src, key string, box sdl.Rect, fit int, urgent bool) webDecodeState {
	tune := webTuneNow()
	webDecode.mu.Lock()
	defer webDecode.mu.Unlock()
	webDecodeStartLocked(tune)
	if webDecode.failed[key] {
		return webDecodeFailed
	}
	meta, _ := doc.imageMeta(src)
	natW, natH := int32(meta.W), int32(meta.H)
	if !meta.HasSize {
		natW, natH = 0, 0
	}
	if natW > 0 && natH > 0 && tune.MaxPixels > 0 && int64(natW)*int64(natH) > tune.MaxPixels {
		webDecode.failed[key] = true
		logWarn("browser", "picture %dx%d is over the %d MP limit, not decoded", natW, natH, tune.MaxPixels>>20)
		return webDecodeFailed
	}
	wantW, wantH := webDecodeNeed(natW, natH, box.W, box.H, fit, webDecode.limit)
	if e := webDecode.entries[key]; e != nil {
		e.seen = webDecode.frame
		if webDecodeCovers([2]int32{e.wantW, e.wantH}, [2]int32{wantW, wantH}) {
			if urgent && !e.urgent && !e.busy {
				webDecodePromoteLocked(key)
			}
			return webDecodePending
		}
		if e.busy {
			// The larger size is asked for again once this one lands.
			return webDecodePending
		}
		webDecodeDropLocked(key)
	}
	data := doc.imageBytes(src)
	if len(data) == 0 {
		return webDecodeFailed
	}
	job := webDecodeJob{key: key, data: data, wantW: wantW, wantH: wantH, natW: natW, natH: natH, gen: webDecode.gen}
	webDecode.entries[key] = &webDecodeEntry{wantW: wantW, wantH: wantH, seen: webDecode.frame, urgent: urgent}
	if urgent {
		webDecode.urgent = append(webDecode.urgent, job)
		if len(webDecode.urgent) > webDecodeQueueMax {
			delete(webDecode.entries, webDecode.urgent[0].key)
			webDecode.urgent = webDecode.urgent[1:]
		}
	} else {
		webDecode.ahead = append(webDecode.ahead, job)
		if len(webDecode.ahead) > webDecodeQueueMax {
			delete(webDecode.entries, webDecode.ahead[0].key)
			webDecode.ahead = webDecode.ahead[1:]
		}
	}
	webDecode.cond.Signal()
	return webDecodePending
}

// webDecodeWants reports whether a texture already made for key is smaller
// than a draw of box now needs, and queues the larger decode when it is.
// Render thread.
func webDecodeWants(doc *webDocument, src, key string, box sdl.Rect, fit int) {
	webDecode.mu.Lock()
	have, ok := webDecode.made[key]
	limit := webDecode.limit
	webDecode.mu.Unlock()
	if !ok || (have[0] == 0 && have[1] == 0) {
		return
	}
	meta, _ := doc.imageMeta(src)
	if !meta.HasSize {
		return
	}
	wantW, wantH := webDecodeNeed(int32(meta.W), int32(meta.H), box.W, box.H, fit, limit)
	if webDecodeCovers(have, [2]int32{wantW, wantH}) {
		return
	}
	webDecodeRequest(doc, src, key, box, fit, true)
}

// webDecodePromoteLocked moves key from the ahead queue to the urgent one.
func webDecodePromoteLocked(key string) {
	for i, job := range webDecode.ahead {
		if job.key != key {
			continue
		}
		webDecode.ahead = append(webDecode.ahead[:i], webDecode.ahead[i+1:]...)
		webDecode.urgent = append(webDecode.urgent, job)
		if e := webDecode.entries[key]; e != nil {
			e.urgent = true
		}
		return
	}
}

// webDecodeDropLocked removes key's queued job, if it has not started.
func webDecodeDropLocked(key string) {
	drop := func(q []webDecodeJob) []webDecodeJob {
		for i, job := range q {
			if job.key == key {
				return append(q[:i], q[i+1:]...)
			}
		}
		return q
	}
	webDecode.urgent = drop(webDecode.urgent)
	webDecode.ahead = drop(webDecode.ahead)
	delete(webDecode.entries, key)
}

// webDecodeCost is the decoded bytes a job holds while it runs: the decoded
// canvas, its RGBA copy and the scaled result.
func webDecodeCost(job webDecodeJob) int64 {
	if job.natW <= 0 || job.natH <= 0 {
		return int64(len(job.data)) * 12
	}
	return int64(job.natW)*int64(job.natH)*8 + int64(job.wantW)*int64(job.wantH)*4
}

// webDecodeWorker is one decode thread.
func webDecodeWorker(tune webTune) {
	for {
		webDecode.mu.Lock()
		var job webDecodeJob
		var cost int64
		for {
			queue := &webDecode.urgent
			if len(*queue) == 0 {
				queue = &webDecode.ahead
			}
			if len(*queue) == 0 {
				webDecode.cond.Wait()
				continue
			}
			job = (*queue)[0]
			e := webDecode.entries[job.key]
			if e == nil || job.gen != webDecode.gen || webDecode.frame-e.seen > webDecodeStaleFrames {
				// Not wanted any more: a page left, a picture scrolled past.
				*queue = (*queue)[1:]
				if e != nil && !e.busy {
					delete(webDecode.entries, job.key)
				}
				continue
			}
			cost = webDecodeCost(job)
			if webDecode.inflight > 0 && webDecode.inflight+cost > tune.DecodeBytes {
				// Wait for the pictures in hand; one picture alone always goes.
				webDecode.cond.Wait()
				continue
			}
			*queue = (*queue)[1:]
			e.busy = true
			webDecode.inflight += cost
			break
		}
		webDecode.mu.Unlock()

		res := webDecodeRun(job, tune)

		webDecode.mu.Lock()
		webDecode.inflight -= cost
		if job.gen == webDecode.gen {
			webDecode.done = append(webDecode.done, res)
		} else if res.surface != nil {
			res.surface.Free()
		}
		webDecode.cond.Broadcast()
		webDecode.mu.Unlock()
	}
}

// webDecodeRun decodes one picture into an ABGR8888 surface of the size the
// job asks for. Decode thread.
func webDecodeRun(job webDecodeJob, tune webTune) webDecodeResult {
	res := webDecodeResult{key: job.key, gen: job.gen}
	rw, err := sdl.RWFromMem(job.data)
	if err != nil || rw == nil {
		return res
	}
	// freesrc: SDL closes the RWops, on failure too.
	src, err := img.LoadRW(rw, true)
	runtime.KeepAlive(job.data)
	if err != nil || src == nil {
		return res
	}
	if tune.MaxPixels > 0 && int64(src.W)*int64(src.H) > tune.MaxPixels {
		src.Free()
		return res
	}
	w, h := min(job.wantW, src.W), min(job.wantH, src.H)
	if w <= 0 || h <= 0 {
		w, h = src.W, src.H
	}
	var out *sdl.Surface
	if w == src.W && h == src.H {
		out, err = src.ConvertFormat(uint32(sdl.PIXELFORMAT_ABGR8888), 0)
		if err != nil {
			out = nil
		}
	} else {
		out = webDownscaleSurface(src, w, h)
	}
	src.Free()
	if out == nil {
		return res
	}
	res.surface, res.w, res.h = out, out.W, out.H
	return res
}

// webPixelLayout is where a surface format keeps each channel of a pixel, in
// bytes from the pixel's start (a < 0: no alpha, opaque).
type webPixelLayout struct {
	bpp, r, g, b, a int
}

// webPixelLayoutOf reads the byte layout of the formats SDL_image hands out,
// on a little-endian machine (arm64 and amd64 both are). ok is false for
// anything else (palettes, 16-bit), which is converted first.
func webPixelLayoutOf(format uint32) (webPixelLayout, bool) {
	switch format {
	case uint32(sdl.PIXELFORMAT_ABGR8888):
		return webPixelLayout{bpp: 4, r: 0, g: 1, b: 2, a: 3}, true
	case uint32(sdl.PIXELFORMAT_ARGB8888):
		return webPixelLayout{bpp: 4, r: 2, g: 1, b: 0, a: 3}, true
	case uint32(sdl.PIXELFORMAT_RGBA8888):
		return webPixelLayout{bpp: 4, r: 3, g: 2, b: 1, a: 0}, true
	case uint32(sdl.PIXELFORMAT_BGRA8888):
		return webPixelLayout{bpp: 4, r: 1, g: 2, b: 3, a: 0}, true
	case uint32(sdl.PIXELFORMAT_RGB888):
		return webPixelLayout{bpp: 4, r: 2, g: 1, b: 0, a: -1}, true
	case uint32(sdl.PIXELFORMAT_BGR888):
		return webPixelLayout{bpp: 4, r: 0, g: 1, b: 2, a: -1}, true
	case uint32(sdl.PIXELFORMAT_RGB24):
		return webPixelLayout{bpp: 3, r: 0, g: 1, b: 2, a: -1}, true
	case uint32(sdl.PIXELFORMAT_BGR24):
		return webPixelLayout{bpp: 3, r: 2, g: 1, b: 0, a: -1}, true
	}
	return webPixelLayout{}, false
}

// webDownscaleSurface scales src down to w×h with an area (box) filter into a
// new ABGR8888 surface: every source pixel lands in exactly one target pixel,
// and colour is averaged by alpha so a transparent edge does not darken. nil
// when a surface cannot be made. Decode thread.
func webDownscaleSurface(src *sdl.Surface, w, h int32) *sdl.Surface {
	if src == nil || w <= 0 || h <= 0 || src.W <= 0 || src.H <= 0 {
		return nil
	}
	layout, ok := webPixelLayoutOf(src.Format.Format)
	conv := src
	if !ok {
		c, err := src.ConvertFormat(uint32(sdl.PIXELFORMAT_ABGR8888), 0)
		if err != nil || c == nil {
			return nil
		}
		conv, layout = c, webPixelLayout{bpp: 4, r: 0, g: 1, b: 2, a: 3}
		defer c.Free()
	}
	dst, err := sdl.CreateRGBSurfaceWithFormat(0, w, h, 32, uint32(sdl.PIXELFORMAT_ABGR8888))
	if err != nil || dst == nil {
		return nil
	}
	if conv.MustLock() {
		if conv.Lock() != nil {
			dst.Free()
			return nil
		}
		defer conv.Unlock()
	}
	webBoxFilter(conv.Pixels(), int(conv.W), int(conv.H), int(conv.Pitch), layout,
		dst.Pixels(), int(w), int(h), int(dst.Pitch))
	return dst
}

// webBoxFilter is the area filter behind webDownscaleSurface, on raw bytes:
// src is sw×sh pixels in layout with sp bytes per row, dst is dw×dh RGBA
// pixels with dp bytes per row. dw ≤ sw and dh ≤ sh.
func webBoxFilter(src []byte, sw, sh, sp int, layout webPixelLayout, dst []byte, dw, dh, dp int) {
	if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 || dw > sw || dh > sh {
		return
	}
	// col maps a source column to its target column; count is how many
	// source columns each target column takes.
	col := make([]int32, sw)
	count := make([]int64, dw)
	for x := 0; x < sw; x++ {
		c := int32(x * dw / sw)
		col[x] = c
		count[c]++
	}
	acc := make([]uint64, dw*4)
	for dy := 0; dy < dh; dy++ {
		y0, y1 := dy*sh/dh, (dy+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		clear(acc)
		for y := y0; y < y1 && y < sh; y++ {
			row := src[y*sp:]
			if layout.a < 0 {
				for x := 0; x < sw; x++ {
					p := row[x*layout.bpp:]
					c := int(col[x]) * 4
					acc[c] += uint64(p[layout.r])
					acc[c+1] += uint64(p[layout.g])
					acc[c+2] += uint64(p[layout.b])
				}
				continue
			}
			for x := 0; x < sw; x++ {
				p := row[x*layout.bpp:]
				a := uint64(p[layout.a])
				if a == 0 {
					continue
				}
				c := int(col[x]) * 4
				acc[c] += uint64(p[layout.r]) * a
				acc[c+1] += uint64(p[layout.g]) * a
				acc[c+2] += uint64(p[layout.b]) * a
				acc[c+3] += a
			}
		}
		rows := uint64(y1 - y0)
		out := dst[dy*dp:]
		for dx := 0; dx < dw; dx++ {
			c := dx * 4
			n := uint64(count[dx]) * rows
			o := out[dx*4:]
			if n == 0 {
				o[0], o[1], o[2], o[3] = 0, 0, 0, 0
				continue
			}
			if layout.a < 0 {
				o[0] = uint8(acc[c] / n)
				o[1] = uint8(acc[c+1] / n)
				o[2] = uint8(acc[c+2] / n)
				o[3] = 255
				continue
			}
			sa := acc[c+3]
			if sa == 0 {
				o[0], o[1], o[2], o[3] = 0, 0, 0, 0
				continue
			}
			o[0] = uint8(acc[c] / sa)
			o[1] = uint8(acc[c+1] / sa)
			o[2] = uint8(acc[c+2] / sa)
			o[3] = uint8(sa / n)
		}
	}
}

// webDecodeUpload turns finished pictures into textures, up to the tune's
// bytes per frame (one picture always goes), and stores them in the picture
// cache. It also notes the renderer's texture limit and counts the page draw
// for the stale-job rule. Called at the start of every page draw. Render
// thread.
func webDecodeUpload(r *sdl.Renderer) {
	tune := webTuneNow()
	if tune.DecodeWorkers <= 0 || r == nil {
		return
	}
	webDecode.mu.Lock()
	webDecode.frame++
	if webDecode.limit == 0 {
		mw, mh := int32(0), int32(0)
		if info, err := r.GetInfo(); err == nil {
			mw, mh = info.MaxTextureWidth, info.MaxTextureHeight
		}
		webDecode.limit = webTuneTextureLimit(tune.MaxSide, mw, mh)
	}
	if len(webDecode.done) == 0 {
		webDecode.mu.Unlock()
		return
	}
	budget := tune.UploadBytes
	n := 0
	for n < len(webDecode.done) {
		res := webDecode.done[n]
		cost := int64(res.w) * int64(res.h) * 4
		if n > 0 && cost > budget {
			break
		}
		budget -= cost
		n++
	}
	take := make([]webDecodeResult, n)
	copy(take, webDecode.done[:n])
	webDecode.done = append(webDecode.done[:0], webDecode.done[n:]...)
	webDecode.mu.Unlock()

	type made struct {
		key  string
		size [2]int32
		ok   bool
	}
	results := make([]made, 0, len(take))
	for _, res := range take {
		if res.surface == nil {
			results = append(results, made{key: res.key})
			continue
		}
		tex, err := r.CreateTextureFromSurface(res.surface)
		res.surface.Free()
		if err != nil || tex == nil {
			results = append(results, made{key: res.key})
			continue
		}
		getThumbnailTextures().Put(res.key, tex, res.w, res.h)
		results = append(results, made{key: res.key, size: [2]int32{res.w, res.h}, ok: true})
	}

	webDecode.mu.Lock()
	for _, m := range results {
		e := webDecode.entries[m.key]
		natural := e != nil && (e.wantW == 0 || e.wantH == 0)
		delete(webDecode.entries, m.key)
		if !m.ok {
			webDecode.failed[m.key] = true
			continue
		}
		if natural {
			// Natural size: nothing larger is ever needed.
			webDecode.made[m.key] = [2]int32{}
		} else {
			webDecode.made[m.key] = m.size
		}
	}
	webDecode.mu.Unlock()
}

// webDecodeReset forgets every queued job, finished picture and failure, for a
// page that is gone (browserReleaseMemory). Jobs already decoding finish and
// are thrown away. Any thread.
func webDecodeReset() {
	webDecode.mu.Lock()
	defer webDecode.mu.Unlock()
	webDecode.gen++
	webDecode.urgent = nil
	webDecode.ahead = nil
	for _, res := range webDecode.done {
		if res.surface != nil {
			res.surface.Free()
		}
	}
	webDecode.done = nil
	if webDecode.entries != nil {
		webDecode.entries = make(map[string]*webDecodeEntry, 64)
		webDecode.failed = make(map[string]bool, 16)
		webDecode.made = make(map[string][2]int32, 64)
	}
	webDecode.aheadLayout = nil
	webImageKeyMemo.Lock()
	webImageKeyMemo.keys = nil
	webImageKeyMemo.Unlock()
}

// webDecodeAheadEvery is how many page draws an unmoved page waits between
// look-ahead scans (pictures that finish downloading meanwhile are found on
// the next one).
const webDecodeAheadEvery = 15

// webDecodeAhead queues the pictures within the tune's window above and below
// the view, behind the ones in view. view is the page area on screen and
// offset the page's scroll in layout pixels. A page that has not moved is
// scanned only every webDecodeAheadEvery draws. Render thread.
func webDecodeAhead(doc *webDocument, layout *webLayout, view sdl.Rect, offset int32) {
	tune := webTuneNow()
	if tune.DecodeWorkers <= 0 || doc == nil || layout == nil || view.H <= 0 {
		return
	}
	webDecode.mu.Lock()
	if webDecode.aheadLayout == layout && webDecode.aheadOffset == offset &&
		webDecode.frame-webDecode.aheadFrame < webDecodeAheadEvery {
		webDecode.mu.Unlock()
		return
	}
	webDecode.aheadLayout, webDecode.aheadOffset, webDecode.aheadFrame = layout, offset, webDecode.frame
	webDecode.mu.Unlock()

	top := offset - int32(float64(view.H)*tune.BehindScreens)
	bottom := offset + view.H + int32(float64(view.H)*tune.AheadScreens)
	visibleTop, visibleBottom := offset, offset+view.H
	seen := make(map[string]bool, 16)
	consider := func(src string, rect sdl.Rect, fit int) {
		if src == "" || rect.W <= 0 || rect.H <= 0 || seen[src] {
			return
		}
		if rect.Y+rect.H <= top || rect.Y >= bottom {
			return
		}
		if rect.Y+rect.H > visibleTop && rect.Y < visibleBottom {
			// In view: the draw asked for it already.
			return
		}
		seen[src] = true
		key := webImageCacheKey(src)
		if getThumbnailTextures().Has(key) {
			return
		}
		if len(doc.imageBytes(src)) == 0 {
			return
		}
		webDecodeRequest(doc, src, key, rect, fit, false)
	}
	for i := range layout.Images {
		im := &layout.Images[i]
		if !im.Invisible {
			consider(im.Key, im.Rect, im.ObjectFit)
		}
	}
	for i := range layout.Backgrounds {
		bg := &layout.Backgrounds[i]
		if bg.Image != "" {
			consider(bg.Image, bg.Rect, bg.ObjectFit)
		}
	}
	for i := range layout.Videos {
		v := &layout.Videos[i]
		if !v.Invisible && v.Thumb != "" {
			consider(v.Thumb, v.Rect, v.ObjectFit)
		}
	}
}
