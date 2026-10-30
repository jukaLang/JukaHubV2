package main

// webpage_image.go — real images in the reader.
//
// The styled web reader used to render every <img> as its alt text: the page
// text read fine, but a recipe without photographs, a docs page without
// diagrams and a store page without box art were all just words. This module
// turns <img> into content:
//
//   * the parser records every loadable image as a block (with the resolved
//     URL or the data: URI) and keeps alt text only when nothing can be drawn;
//   * the page-load pass fetches the images once, concurrently and under
//     strict per-page and per-byte limits, and records the natural sizes the
//     layout needs;
//   * the layout reserves row slots for every image, like the rule and rects
//     it already reserved space for;
//   * draw paints each image through the shared texture cache, falling back
//     to a calm placeholder card when an image could not be fetched.
//
// Everything here is bounded: a page of twenty 4K hero banners must not be
// able to starve a 1 GB handheld.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// Collect/fetch limits. webMaxImageBytes keeps a single huge photograph from
// dominating; the total budget keeps a page of hero banners from doing it via
// volume instead.
const (
	webMaxImagesPerPage      = 16
	webMaxImageBytes         = 1 << 20 // 1 MiB per image
	webMaxImagesBytes        = 4 << 20 // 4 MiB of images per page
	webMaxVideoThumbsPerPage = 8       // one page never fetches more video thumbnails
	webImageFetchTimeout     = 8 * time.Second
	// webImagePassTimeout bounds the whole image pass, not one image. The page
	// text is published after this pass, so a page of slow hero banners must
	// not keep the reader on the loading card for eight seconds each; anything
	// still in flight when it expires is drawn as a placeholder.
	webImagePassTimeout = 6 * time.Second
	webMinImagePx       = 16 // smallest drawable image
)

// webImageMeta is one image's decoded-at-fetch natural size. No HasSize means
// "we could not read the pixel size", and the layout falls back to ratios.
type webImageMeta struct {
	HasSize bool
	W       int
	H       int
}

// webImagePos is one image in a laid-out page: its key (resolved URL or data
// URI) and the rect reserved for it, in layout-local coordinates like Rects.
type webImagePos struct {
	Key  string
	Rect sdl.Rect
}

// webImageURL resolves an img src into either a data:image/...;base64 URI or
// an http(s) URL relative to the document. Anything else (svg data URIs, ftp,
// javascript, or a relative URL on a base-less page) returns "", which keeps
// alt text rendering instead of a dead weight in the layout.
func (p *webParser) webImageURL(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	if strings.HasPrefix(src, "data:image/") {
		if strings.Contains(src, ";base64,") {
			return src
		}
		return ""
	}
	base, err := url.Parse(p.doc.URL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(src)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(ref)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	return abs.String()
}

// webImageCacheKey turns an image key into a thumbnail-cache key. Data URIs
// are far too large to be map keys, so everything hashes.
func webImageCacheKey(src string) string {
	sum := sha1.Sum([]byte(src))
	return "webimg:" + hex.EncodeToString(sum[:])
}

// imageBytes returns one image's fetched bytes under the image mutex.
func (d *webDocument) imageBytes(key string) []byte {
	if d == nil {
		return nil
	}
	d.imageMu.Lock()
	defer d.imageMu.Unlock()
	if d.Images == nil {
		return nil
	}
	return d.Images[key]
}

// imageMeta returns one image's recorded natural size under the image mutex.
func (d *webDocument) imageMeta(key string) (webImageMeta, bool) {
	if d == nil {
		return webImageMeta{}, false
	}
	d.imageMu.Lock()
	defer d.imageMu.Unlock()
	m, ok := d.ImageMeta[key]
	return m, ok
}

// decodeWebImageMeta decodes bytes far enough to learn the natural size, then
// frees the surface: the layout needs small integers, not pixels.
func decodeWebImageMeta(data []byte) webImageMeta {
	if len(data) == 0 {
		return webImageMeta{}
	}
	rwops, err := sdl.RWFromMem(data)
	if err != nil {
		return webImageMeta{}
	}
	surface, err := img.LoadRW(rwops, true)
	if err != nil || surface == nil {
		// LoadRW with freesrc=true frees it even on failure; ignore the
		// double-free risk by only closing explicitly on the success path's
		// own freesrc flag.
		_ = rwops
		return webImageMeta{}
	}
	m := webImageMeta{HasSize: true, W: int(surface.W), H: int(surface.H)}
	return m
}

// decodeWebImageDataURI decodes a data:image/...;base64 URI into raw bytes
// (plus its meta) without touching the network.
func decodeWebImageDataURI(src string) ([]byte, webImageMeta, bool) {
	comma := strings.Index(src, ";base64,")
	if comma < 0 {
		return nil, webImageMeta{}, false
	}
	b64 := src[comma+len(";base64,"):]
	if b64 == "" || len(b64) > webMaxImageBytes*2 {
		return nil, webImageMeta{}, false
	}
	// Standard padding first; URI-safe alphabets (a common CDN slip, often
	// unpadded) fall back so nobody's images fail on a naming detail.
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(b64)
	}
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(b64)
	}
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(b64)
	}
	if err != nil || len(raw) == 0 || len(raw) > webMaxImageBytes {
		return nil, webImageMeta{}, false
	}
	return raw, decodeWebImageMeta(raw), true
}

// webImageDimsFromBlock works out the layout-time box for one image. Attribute
// sizes scale like CSS px; the recorded natural pixel size wins when it is
// larger, because authors put full-resolution photos behind small attributes.
func webImageDimsFromBlock(b webBlock, doc *webDocument, scale, contentW int32) (w, h int32) {
	var meta webImageMeta
	if doc != nil {
		meta, _ = doc.imageMeta(b.ImageSrc)
	}
	ratio := 0.0
	if b.ImageW > 0 && b.ImageH > 0 {
		ratio = b.ImageH / b.ImageW
	}
	if ratio == 0 && meta.HasSize && meta.W > 0 {
		ratio = float64(meta.H) / float64(meta.W)
	}
	w = int32(0)
	if b.ImageW > 0 {
		w = int32(b.ImageW * float64(scale))
	}
	if meta.HasSize && int32(meta.W) > w {
		w = int32(meta.W)
	}
	if w <= 0 {
		// No size hint at all: a comfortable reading width beats a sliver.
		w = contentW
	}
	if w > contentW {
		w = contentW
	}
	if w < webMinImagePx {
		w = webMinImagePx
	}
	if ratio > 0 {
		h = int32(float64(w) * ratio)
	} else {
		h = w * 3 / 4
	}
	// A portrait photograph taller than a couple of screens pushes a hundred
	// blank rows onto a page the reader scrolls through empty. Cap the box
	// and keep the ratio.
	maxH := contentW * 4 / 3
	if maxH < 360 {
		maxH = 360
	}
	if h > maxH {
		h = maxH
		if ratio > 0 {
			if w = int32(float64(h) / ratio); w > contentW {
				w = contentW
			}
		}
	}
	if h < webMinImagePx {
		h = webMinImagePx
	}
	return w, h
}

// browserFaviconURL resolves a page's declared icon, falling back to the
// origin's conventional favicon. Never downgrade an HTTPS page to HTTP.
func browserFaviconURL(source string, base *url.URL) string {
	if base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" {
		return ""
	}
	pos := 0
	for pos < len(source) {
		i := webIndexFold(source[pos:], "<link")
		if i < 0 {
			break
		}
		start := pos + i
		end := findTextBrowserTagEnd(source, start+1)
		if end < 0 {
			break
		}
		tag := source[start+1 : end]
		pos = end + 1
		found := false
		for _, rel := range strings.Fields(strings.ToLower(htmlTagAttribute(tag, "rel"))) {
			if rel == "icon" {
				found = true
			}
		}
		if !found {
			continue
		}
		ref, err := url.Parse(strings.TrimSpace(htmlTagAttribute(tag, "href")))
		if err != nil || ref.String() == "" {
			continue
		}
		icon := base.ResolveReference(ref)
		if (icon.Scheme == "http" || icon.Scheme == "https") && icon.Hostname() != "" && icon.User == nil && !(base.Scheme == "https" && icon.Scheme != "https") {
			return icon.String()
		}
	}
	return base.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()
}

func drawBrowserFavicon(r *sdl.Renderer, config *Config, doc *webDocument, rect sdl.Rect) {
	if doc != nil && doc.Favicon != "" && len(doc.imageBytes(doc.Favicon)) > 0 {
		drawWebImage(r, config, doc, webImagePos{Key: doc.Favicon, Rect: rect}, rect.X, rect.Y)
		return
	}
	// A neutral globe is the fallback, not a claim about site identity.
	r.SetDrawColor(155, 170, 195, 255)
	strokeCircle(r, rect.X+rect.W/2, rect.Y+rect.H/2, 7, sdl.Color{R: 155, G: 170, B: 195, A: 255})
	r.DrawLine(rect.X+4, rect.Y+rect.H/2, rect.X+rect.W-4, rect.Y+rect.H/2)
}

// fetchWebImages loads every <img> in a parsed document once, concurrently
// and under the per-page/per-byte limits. It also records the natural sizes
// the layout needs, and always stores an entry per key so the layout never
// has to probe a second time.
func fetchWebImages(doc *webDocument) {
	if doc == nil {
		return
	}
	seen := make(map[string]bool)
	srcs := make([]string, 0, webMaxImagesPerPage)
	for _, b := range doc.Blocks {
		if !b.Image || b.ImageSrc == "" || seen[b.ImageSrc] {
			continue
		}
		if len(srcs) >= webMaxImagesPerPage {
			break
		}
		seen[b.ImageSrc] = true
		srcs = append(srcs, b.ImageSrc)
	}
	// Video play cards (YouTube embeds) carry a thumbnail URL on the block;
	// the same pass fetches it so a card paints the video's real artwork with
	// no extra round-trips beyond what <img> already paid.
	thumbSrcs := make([]string, 0, webMaxVideoThumbsPerPage)
	thumbSeen := make(map[string]bool)
	for _, b := range doc.Blocks {
		if !b.Video || b.VideoThumb == "" || thumbSeen[b.VideoThumb] {
			continue
		}
		if len(thumbSrcs) >= webMaxVideoThumbsPerPage {
			break
		}
		thumbSeen[b.VideoThumb] = true
		thumbSrcs = append(thumbSrcs, b.VideoThumb)
	}
	srcs = append(srcs, thumbSrcs...)
	if doc.Favicon != "" && !seen[doc.Favicon] && !thumbSeen[doc.Favicon] {
		srcs = append(srcs, doc.Favicon)
	}
	if len(srcs) == 0 {
		return
	}

	images := make(map[string][]byte, len(srcs))
	fetched := make(map[string]webImageMeta, len(srcs))
	var budget int64 = webMaxImagesBytes
	var mu sync.Mutex
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), webImagePassTimeout)
	defer cancel()

	for _, src := range srcs {
		// Data URIs decode inline: no network, no budget.
		if data, m, ok := decodeWebImageDataURI(src); ok {
			images[src] = data
			fetched[src] = m
		}
	}
	for _, src := range srcs {
		if _, ok := images[src]; ok {
			continue
		}
		if atomic.LoadInt64(&budget) <= 0 {
			// Budget exhausted: record the key so the layout still reserves
			// its box, but do not pay for a 17th download.
			mu.Lock()
			fetched[src] = webImageMeta{}
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(src string) {
			defer wg.Done()
			body := fetchWebImageBytes(ctx, doc.URL, src)
			if len(body) == 0 {
				mu.Lock()
				fetched[src] = webImageMeta{}
				mu.Unlock()
				return
			}
			atomic.AddInt64(&budget, -int64(len(body)))
			m := decodeWebImageMeta(body)
			mu.Lock()
			images[src] = body
			fetched[src] = m
			mu.Unlock()
		}(src)
	}
	wg.Wait()

	// Anything scheduled but never fetched still needs an entry so the layout
	// reserves a box (and drawWebImage knows to placeholder it).
	mu.Lock()
	for _, src := range srcs {
		if _, ok := fetched[src]; !ok {
			fetched[src] = webImageMeta{}
		}
	}
	mu.Unlock()

	doc.imageMu.Lock()
	doc.Images = images
	doc.ImageMeta = fetched
	doc.imageMu.Unlock()
}

// fetchWebImageBytes downloads one image with the reader's browser identity
// and only accepts bytes the device can decode as an image, so an HTML error
// page never reaches the decoder and a text response never eats the budget.
// The caller's context bounds the whole pass; this one only caps one request.
func fetchWebImageBytes(parent context.Context, pageURL, address string) []byte {
	// Ad images and tracking pixels are dropped before a connection is opened:
	// a 1x1 pixel still costs a TLS handshake on handheld Wi-Fi. A page the
	// user excepted fetches its images too.
	if browserRequestBlocked(pageURL, address) {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, webImageFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "JukaHub Browser/1.1")
	// Images share the page's jar: a picture behind a session cookie (a
	// private avatar, a captcha-free gallery) otherwise drew as a placeholder.
	client := browserHTTPClient(webImageFetchTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.Contains(ct, "image") {
		return nil
	}
	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, io.LimitReader(resp.Body, webMaxImageBytes+1)); err != nil {
		return nil
	}
	if buf.Len() == 0 || buf.Len() > webMaxImageBytes {
		return nil
	}
	return buf.Bytes()
}

// layoutImage reserves a page layout's rows for one image block and records
// the box the painter draws into. It runs inside layoutBlocks' block loop, so
// margins, alignment and the webMaxRows cap behave exactly like text.
func (l *webLayouter) layoutImage(b webBlock, padX, contentW int32) {
	layout, rowH := l.layout, l.rowH
	if rowH <= 0 || contentW <= 0 {
		return
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	w, h := webImageDimsFromBlock(b, l.doc, int32(scale), contentW)
	idx := int32(len(layout.Rows))
	slots := int32((h + rowH - 1) / rowH)
	if slots < 1 {
		slots = 1
	}
	if idx+slots > webMaxRows {
		slots = webMaxRows - idx
		if slots < 1 {
			return
		}
		h = slots * rowH
	}
	x := padX
	switch b.Align {
	case 1: // center
		x = padX + (contentW-w)/2
	case 2: // right
		x = padX + (contentW - w)
	}
	if x < padX {
		x = padX
	}
	layout.Images = append(layout.Images, webImagePos{
		Key:  b.ImageSrc,
		Rect: sdl.Rect{X: x, Y: idx * rowH, W: w, H: h},
	})
	for i := int32(0); i < slots && len(layout.Rows) < webMaxRows; i++ {
		layout.Rows = append(layout.Rows, webRow{Y: (idx + i) * rowH})
	}
}

// htmlTagAttributeFloat reads a numeric HTML attribute ("100px", "100" or a
// percentage-looking "100%" number) as a plain float. Zero for anything
// absent or nonsense, so the layout falls back to the natural size.
func htmlTagAttributeFloat(attrs, name string) float64 {
	raw := strings.TrimSpace(htmlTagAttribute(attrs, name))
	raw = strings.TrimSuffix(strings.TrimSuffix(raw, "px"), "%")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var v float64
	if _, err := fmt.Sscanf(raw, "%g", &v); err != nil {
		return 0
	}
	if v < 0 {
		return 0
	}
	return v
}

// drawWebImage paints one laid-out image. Bytes come from the document itself
// (the fetch already ran), decoding happens once through the shared texture
// cache, and a missing image draws the same calm placeholder the layout
// reserved a box for.
func drawWebImage(r *sdl.Renderer, config *Config, doc *webDocument, im webImagePos, x, y int32) {
	rect := sdl.Rect{X: x, Y: y, W: im.Rect.W, H: im.Rect.H}
	if rect.W <= 0 || rect.H <= 0 {
		return
	}
	key := webImageCacheKey(im.Key)
	if tex := getThumbnailTextures().Get(key); tex != nil {
		_ = r.Copy(tex, nil, &rect)
		return
	}
	data := doc.imageBytes(im.Key)
	if len(data) == 0 {
		drawWebImagePlaceholder(r, config, rect)
		return
	}
	rwops, err := sdl.RWFromMem(data)
	if err != nil || rwops == nil {
		drawWebImagePlaceholder(r, config, rect)
		return
	}
	tex, err := img.LoadTextureRW(r, rwops, false)
	rwops.Close()
	if err != nil || tex == nil {
		drawWebImagePlaceholder(r, config, rect)
		return
	}
	tw, th := int32(0), int32(0)
	if _, _, qw, qh, qerr := tex.Query(); qerr == nil {
		tw, th = qw, qh
	}
	getThumbnailTextures().Put(key, tex, tw, th)
	if err := r.Copy(tex, nil, &rect); err != nil {
		drawWebImagePlaceholder(r, config, rect)
	}
}

// copyWebImageCover blits one texture across an image/video card, cropping
// its center rectangle so a narrower source covers the target without
// letterboxing.
func copyWebImageCover(r *sdl.Renderer, tex *sdl.Texture, rect sdl.Rect) {
	tw, th := int32(0), int32(0)
	if _, _, qw, qh, qerr := tex.Query(); qerr == nil {
		tw, th = qw, qh
	}
	var src *sdl.Rect
	if tw > 0 && th > 0 {
		targetAspect := float64(rect.W) / float64(rect.H)
		srcAspect := float64(tw) / float64(th)
		if srcAspect > targetAspect { // source too wide: crop its sides
			sw := int32(float64(th)*targetAspect + 0.5)
			src = &sdl.Rect{X: (tw - sw) / 2, Y: 0, W: sw, H: th}
		} else if srcAspect < targetAspect { // source too tall: crop top/bottom
			sh := int32(float64(tw)/targetAspect + 0.5)
			src = &sdl.Rect{X: 0, Y: (th - sh) / 2, W: tw, H: sh}
		}
	}
	_ = r.Copy(tex, src, &rect)
}

// drawWebImagePlaceholder fills a failed image's reserved box with quiet
// chrome: the same greys the horizontal rule uses, so a broken image reads as
// "unloaded here" instead of flashing black or disappearing entirely.
func drawWebImagePlaceholder(r *sdl.Renderer, config *Config, rect sdl.Rect) {
	if rect.W <= 0 || rect.H <= 0 || r == nil {
		return
	}
	_ = config
	fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 2, sdl.Color{R: 236, G: 239, B: 244, A: 255})
	strokeRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 2, 1, sdl.Color{R: 200, G: 206, B: 216, A: 255})
}
