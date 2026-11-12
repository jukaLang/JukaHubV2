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
	"image"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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
	webImageFetchWorkers     = 4 // concurrent image downloads per page
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
	Key            string
	Rect           sdl.Rect
	ObjectFit      int
	ObjectPosition string
	Invisible      bool
	Clip           sdl.Rect
	HasClip        bool
	Opacity        float64
}

// webSrcsetTargetWidth is the design-pixel width a responsive image should
// cover on the handheld. The reader lays pages out around this width, so a
// "1280w" candidate is a better fit than the tiny fallback many pages put in
// src for old browsers.
const (
	webSrcsetTargetWidth   = 960.0
	webSrcsetTargetDensity = 2.0
)

// webSrcsetCandidate is one entry from an img srcset attribute. Exactly one of
// W (a "640w" width descriptor) and D (a "2x" density descriptor) is usually
// set; an entry without a descriptor is treated as 1x.
type webSrcsetCandidate struct {
	URL string
	W   float64
	D   float64
}

// webSplitSrcset splits entries without cutting a data URI in half: its
// base64 payload contains a comma that is part of the URL, not a separator.
func webSplitSrcset(srcset string) []string {
	var entries []string
	start, inData := 0, false
	for i := 0; i < len(srcset); i++ {
		c := srcset[i]
		if !inData && strings.HasPrefix(srcset[i:], "data:") {
			inData = true
		}
		if inData && webAttrSpaceByte(c) {
			inData = false
		}
		if c == ',' && (!inData || i+1 < len(srcset) && webAttrSpaceByte(srcset[i+1])) {
			entries = append(entries, srcset[start:i])
			start = i + 1
			inData = false
		}
	}
	return append(entries, srcset[start:])
}

// webSrcsetPick chooses the srcset entry closest to the reader's width. Width
// descriptors win over density descriptors when both appear (mixed srcsets are
// invalid HTML). If every candidate is narrower than the target, the widest is
// still better than the fallback; if every one is wider, the smallest wastes
// the least bandwidth.
func webSrcsetCandidates(srcset string) []webSrcsetCandidate {
	var candidates []webSrcsetCandidate
	for _, entry := range webSplitSrcset(srcset) {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			continue
		}
		c := webSrcsetCandidate{URL: fields[0], D: 1}
		if len(fields) > 1 {
			desc := strings.ToLower(fields[1])
			switch {
			case strings.HasSuffix(desc, "w"):
				if n, err := strconv.ParseFloat(strings.TrimSuffix(desc, "w"), 64); err == nil && n > 0 {
					c.W, c.D = n, 0
				}
			case strings.HasSuffix(desc, "x"):
				if n, err := strconv.ParseFloat(strings.TrimSuffix(desc, "x"), 64); err == nil && n > 0 {
					c.D = n
				}
			}
		}
		candidates = append(candidates, c)
	}
	return candidates
}

func webSrcsetPick(srcset string, targetW float64) string {
	candidates := webSrcsetCandidates(srcset)
	if len(candidates) == 0 {
		return ""
	}

	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.W > 0 {
			if best.W <= 0 || (best.W < targetW && c.W > best.W) || (c.W >= targetW && c.W < best.W) {
				best = c
			}
			continue
		}
		if best.W <= 0 && c.W <= 0 {
			if best.D < webSrcsetTargetDensity && c.D > best.D {
				best = c
			} else if best.D >= webSrcsetTargetDensity && c.D >= webSrcsetTargetDensity && c.D < best.D {
				best = c
			}
		}
	}
	return best.URL
}

// webSrcsetHasWidth reports whether a srcset uses width descriptors. Only
// those need a `sizes` target; a density srcset is chosen independently of the
// rendered width.
func webSrcsetHasWidth(srcset string) bool {
	for _, c := range webSrcsetCandidates(srcset) {
		if c.W > 0 {
			return true
		}
	}
	return false
}

// webSizesLength resolves one entry of an img sizes attribute. The reader has
// a real CSS viewport for media queries, so viewport units can be used here
// even though the styled reader's layout rejects them elsewhere. calc() is
// evaluated with those same viewport dimensions, not merely stripped down to
// its px/em operands.
func webSizesLength(value string, vw, vh float64) (float64, bool) {
	return webSizesLengthIn(value, vw, vh, vw, vh)
}

// webSizesLengthIn is webSizesLength with the dimensions of the nearest query
// container. With no container, callers pass the viewport, which is the CSS
// fallback for container-relative units.
func webSizesLengthIn(value string, vw, vh, cw, ch float64) (float64, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "auto" {
		return 0, false
	}
	// sizes accepts the full CSS math family. Evaluating it through the calc
	// parser also covers min/max/clamp nested inside calc and nested functions
	// inside their arguments, with viewport and container units available.
	for _, name := range []string{"calc(", "min(", "max(", "clamp("} {
		if strings.HasPrefix(value, name) && strings.HasSuffix(value, ")") {
			if px, ok := webEvalCalcContainer(value, 16, vw, vh, cw, ch); ok && px >= 0 {
				return px, true
			}
			return 0, false
		}
	}
	unit := func(suffix string) (float64, bool) {
		if !strings.HasSuffix(value, suffix) {
			return 0, false
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, suffix)), 64)
		return n, err == nil && n >= 0
	}
	switch {
	case strings.HasSuffix(value, "vw"):
		if n, ok := unit("vw"); ok {
			return vw * n / 100, true
		}
	case strings.HasSuffix(value, "vh"):
		if n, ok := unit("vh"); ok {
			return vh * n / 100, true
		}
	case strings.HasSuffix(value, "vmin"):
		if n, ok := unit("vmin"); ok {
			return min(vw, vh) * n / 100, true
		}
	case strings.HasSuffix(value, "vmax"):
		if n, ok := unit("vmax"); ok {
			return max(vw, vh) * n / 100, true
		}
	case strings.HasSuffix(value, "cqw"), strings.HasSuffix(value, "cqi"):
		suffix := "cqw"
		if strings.HasSuffix(value, "cqi") {
			suffix = "cqi"
		}
		if n, ok := unit(suffix); ok {
			return cw * n / 100, true
		}
	case strings.HasSuffix(value, "cqh"), strings.HasSuffix(value, "cqb"):
		suffix := "cqh"
		if strings.HasSuffix(value, "cqb") {
			suffix = "cqb"
		}
		if n, ok := unit(suffix); ok {
			return ch * n / 100, true
		}
	case strings.HasSuffix(value, "cqmin"):
		if n, ok := unit("cqmin"); ok {
			return min(cw, ch) * n / 100, true
		}
	case strings.HasSuffix(value, "cqmax"):
		if n, ok := unit("cqmax"); ok {
			return max(cw, ch) * n / 100, true
		}
	}
	if px := webLength(value, 16); px > 0 {
		return px, true
	}
	return 0, false
}

// webSizesEntry separates a sizes entry into its optional media condition and
// its length. A condition can contain parentheses and the length can be a math
// function, so the split is on the matching top-level close parenthesis.
func webSizesEntry(entry string) (condition, value string, ok bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", "", false
	}
	if entry[0] != '(' {
		return "", entry, true
	}
	depth := 0
	for i := 0; i < len(entry); i++ {
		switch entry[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return entry[:i+1], strings.TrimSpace(entry[i+1:]), true
			}
		}
	}
	return "", "", false
}

// webSizesTarget picks the rendered image width from a sizes attribute: the
// first media condition that matches wins, and a bare length is the fallback
// when no condition does. Zero means no usable size was supplied.
func webSizesTarget(sizes string, vw, vh float64) float64 {
	return webSizesTargetIn(sizes, vw, vh, vw, vh)
}

// webSizesTargetIn evaluates sizes with viewport dimensions for media
// conditions and container dimensions for cq* lengths.
func webSizesTargetIn(sizes string, vw, vh, cw, ch float64) float64 {
	fallback := 0.0
	for _, part := range webSplitSelectorList(sizes) {
		condition, value, ok := webSizesEntry(part)
		if !ok {
			continue
		}
		px, valid := webSizesLengthIn(value, vw, vh, cw, ch)
		if !valid {
			continue
		}
		if condition == "" {
			fallback = px
			continue
		}
		if webMediaQueryApplies(condition, vw, vh) {
			return px
		}
	}
	return fallback
}

// webSrcsetPickSized applies sizes only to width-descriptor srcsets. A dense
// srcset keeps the fixed target, because its descriptors are already relative
// to the output density rather than to the rendered width.
func webSrcsetPickSized(srcset, sizes string) string {
	vw, vh := webMediaViewport()
	return webSrcsetPickSizedIn(srcset, sizes, vw, vh)
}

// webSrcsetPickSizedIn selects with explicit container dimensions. The sizes
// list still judges media conditions against the viewport, as CSS does.
func webSrcsetPickSizedIn(srcset, sizes string, cw, ch float64) string {
	target := webSrcsetTargetWidth
	if webSrcsetHasWidth(srcset) {
		vw, vh := webMediaViewport()
		if rendered := webSizesTargetIn(sizes, vw, vh, cw, ch); rendered > 0 {
			target = rendered
		}
	}
	return webSrcsetPick(srcset, target)
}

// webImageContainerSize resolves the nearest query container around the image
// being parsed. Widths follow the ancestor chain the way an auto-width block
// does; without a container, the small viewport is the CSS fallback.
func (p *webParser) webImageContainerSize() (w, h float64) {
	w, h = webMediaViewport()
	idx := -1
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].style.ContainerType != 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return w, h
	}
	for i := 0; i <= idx; i++ {
		st := p.stack[i].style
		if st.WidthPct > 0 {
			w = w * st.WidthPct / 100
		} else if st.Width > 0 {
			w = st.Width
		}
		if st.Height > 0 {
			h = st.Height
		}
	}
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	return w, h
}

func (p *webParser) webSrcsetPickForImage(srcset, sizes string) string {
	cw, ch := p.webImageContainerSize()
	return webSrcsetPickSizedIn(srcset, sizes, cw, ch)
}

// webImageTypeSupported reports whether the <source type> MIME is one this
// build can actually decode. Picture art direction must not pick an AVIF or
// SVG source only to render the placeholder.
func webImageTypeSupported(value string) bool {
	mime := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	switch mime {
	case "image/png", "image/apng", "image/jpeg", "image/jpg", "image/gif", "image/bmp":
		return true
	case "image/webp":
		return webpDecodable()
	}
	return false
}

// considerPictureSource evaluates one <source> in document order. A matching
// source stays chosen and later sources are ignored; a source whose media or
// type does not match, or whose srcset cannot be resolved, lets the walk move
// on to the next one.
func (p *webParser) considerPictureSource(attrs string) {
	n := len(p.pictures)
	if n == 0 || p.pictures[n-1].selected {
		return
	}
	if media := htmlAttrValue(htmlTagAttribute(attrs, "media")); media != "" && !webMediaBlockApplies(media) {
		return
	}
	if typ := htmlAttrValue(htmlTagAttribute(attrs, "type")); typ != "" && !webImageTypeSupported(typ) {
		return
	}
	srcset := htmlAttrValue(htmlTagAttribute(attrs, "srcset"))
	if srcset == "" {
		return
	}
	picked := p.webSrcsetPickForImage(srcset, htmlAttrValue(htmlTagAttribute(attrs, "sizes")))
	if picked == "" {
		return
	}
	if resolved := p.webImageURL(picked); resolved != "" {
		p.pictures[n-1].chosen = resolved
		p.pictures[n-1].selected = true
	}
}

// webImageSource picks the URL an <img> should use: the matching <picture>
// source first, then its own srcset, then src. A bad srcset candidate falls
// back to src rather than dropping the image entirely.
func (p *webParser) webImageSource(attrs string) string {
	if n := len(p.pictures); n > 0 && p.pictures[n-1].selected {
		return p.pictures[n-1].chosen
	}
	if srcset := htmlAttrValue(htmlTagAttribute(attrs, "srcset")); srcset != "" {
		picked := p.webSrcsetPickForImage(srcset, htmlAttrValue(htmlTagAttribute(attrs, "sizes")))
		if resolved := p.webImageURL(picked); picked != "" && resolved != "" {
			return resolved
		}
	}
	return p.webImageURL(htmlAttrValue(htmlTagAttribute(attrs, "src")))
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

// decodeWebImageMeta learns an image's natural size: the layout needs two small
// integers, not pixels. PNG, JPEG and GIF answer from their header and WebP
// from its RIFF chunk, so nothing is decoded; only an exotic format falls back
// to a full decode. That fallback used to be the only path, and it never freed
// the surface — every picture on every page leaked its decoded pixels.
func decodeWebImageMeta(data []byte) webImageMeta {
	if len(data) == 0 {
		return webImageMeta{}
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
		return webImageMeta{HasSize: true, W: cfg.Width, H: cfg.Height}
	}
	if w, h, ok := webpImageSize(data); ok {
		return webImageMeta{HasSize: true, W: w, H: h}
	}
	rwops, err := sdl.RWFromMem(data)
	if err != nil {
		return webImageMeta{}
	}
	// freesrc=true: SDL closes the RWops, on failure too.
	surface, err := img.LoadRW(rwops, true)
	if err != nil || surface == nil {
		return webImageMeta{}
	}
	defer surface.Free()
	return webImageMeta{HasSize: true, W: int(surface.W), H: int(surface.H)}
}

// webpImageSize reads a WebP's canvas size from its first chunk (lossy VP8,
// lossless VP8L or extended VP8X) without decoding it.
func webpImageSize(data []byte) (int, int, bool) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	var w, h int
	switch string(data[12:16]) {
	case "VP8 ":
		// Frame tag (3 bytes) and start code (3 bytes) precede two 14-bit sizes.
		if data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, false
		}
		w = int(data[26]) | int(data[27]&0x3f)<<8
		h = int(data[28]) | int(data[29]&0x3f)<<8
	case "VP8L":
		if data[20] != 0x2f {
			return 0, 0, false
		}
		b0, b1, b2, b3 := int(data[21]), int(data[22]), int(data[23]), int(data[24])
		w = 1 + (b0 | (b1&0x3f)<<8)
		h = 1 + (b1>>6 | b2<<2 | (b3&0x0f)<<10)
	case "VP8X":
		w = 1 + (int(data[24]) | int(data[25])<<8 | int(data[26])<<16)
		h = 1 + (int(data[27]) | int(data[28])<<8 | int(data[29])<<16)
	default:
		return 0, 0, false
	}
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
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
	aspect := b.Style.AspectRatio
	cssW, cssH := int32(0), int32(0)
	if b.Style.WidthPct > 0 {
		cssW = int32(float64(contentW) * b.Style.WidthPct / 100)
	} else if b.Style.Width > 0 {
		cssW = int32(b.Style.Width * float64(scale))
	}
	if b.Style.Height > 0 {
		cssH = int32(b.Style.Height * float64(scale))
	}
	w = int32(0)
	if cssW > 0 {
		w = cssW
	} else {
		if b.ImageW > 0 {
			w = int32(b.ImageW * float64(scale))
		}
		if meta.HasSize && int32(meta.W) > w {
			w = int32(meta.W)
		}
	}
	if w <= 0 && cssH > 0 && aspect > 0 {
		w = int32(float64(cssH) * aspect)
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
	switch {
	case aspect > 0 && cssH > 0:
		h = cssH
	case aspect > 0:
		h = int32(float64(w) / aspect)
	case cssH > 0:
		h = cssH
	case ratio > 0:
		h = int32(float64(w) * ratio)
	default:
		h = w * 3 / 4
	}
	// width/min-width/max-width and the height constraints apply to the
	// replaced element's box too. Keep the source ratio when one side moves.
	scaleF := float64(scale)
	if b.Style.BoxSizing == 1 && (b.Style.Width > 0 || b.Style.WidthPct > 0) {
		w -= int32((b.Indent + b.Style.IndentRight) * scaleF)
		if w < webMinImagePx {
			w = webMinImagePx
		}
	}
	resizeW := func(next int32) {
		if next == w || w <= 0 || next < webMinImagePx {
			return
		}
		h = int32(float64(h) * float64(next) / float64(w))
		w = next
	}
	resizeH := func(next int32) {
		if next == h || h <= 0 || next < webMinImagePx {
			return
		}
		w = int32(float64(w) * float64(next) / float64(h))
		h = next
		if w > contentW {
			w = contentW
		}
	}
	if maxW := webConstraintLength(b.Style.MaxWidth, b.Style.MaxWidthPct, contentW, scaleF); maxW > 0 && float64(w)/scaleF > maxW {
		resizeW(int32(math.Round(maxW * scaleF)))
	}
	if minW := webConstraintLength(b.Style.MinWidth, b.Style.MinWidthPct, contentW, scaleF); minW > 0 && float64(w)/scaleF < minW {
		resizeW(int32(math.Round(minW * scaleF)))
	}
	if maxH := b.Style.MaxHeight * scaleF; maxH > 0 && float64(h) > maxH {
		resizeH(int32(math.Round(maxH)))
	}
	if minH := b.Style.MinHeight * scaleF; minH > 0 && float64(h) < minH {
		resizeH(int32(math.Round(minH)))
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
		switch {
		case aspect > 0:
			w = int32(float64(h) * aspect)
		case ratio > 0:
			w = int32(float64(h) / ratio)
		}
		if w > contentW {
			w = contentW
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
		ref, err := url.Parse(htmlAttrValue(htmlTagAttribute(tag, "href")))
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
		drawWebImage(r, config, doc, webImagePos{Key: doc.Favicon, Rect: rect, Opacity: 1}, rect.X, rect.Y)
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
	for _, b := range doc.Blocks {
		for i := 0; i < b.Style.BackgroundLayerCount && i < webMaxBackgroundLayers; i++ {
			src := b.Style.BackgroundLayers[i].Image
			if src == "" || seen[src] {
				continue
			}
			if len(srcs) >= webMaxImagesPerPage {
				break
			}
			seen[src] = true
			srcs = append(srcs, src)
		}
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

	var remote []string
	for _, src := range srcs {
		// Data URIs decode inline: no network, no budget.
		if data, m, ok := decodeWebImageDataURI(src); ok {
			images[src] = data
			fetched[src] = m
			continue
		}
		remote = append(remote, src)
	}
	// A few workers, each reserving the worst case from the page budget before
	// it downloads and handing back what the image did not use. The budget
	// used to be read only when a fetch was scheduled — and every fetch was
	// scheduled before the first one finished, so all of them ran and the
	// 4 MiB page cap never held.
	queue := make(chan string)
	for i := 0; i < webImageFetchWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for src := range queue {
				mu.Lock()
				reserve := int64(webMaxImageBytes)
				if budget < reserve {
					reserve = budget
				}
				if reserve <= 0 {
					// Budget spent: the key still gets an entry so the layout
					// reserves its box and the painter draws a placeholder.
					fetched[src] = webImageMeta{}
					mu.Unlock()
					continue
				}
				budget -= reserve
				mu.Unlock()

				body := fetchWebImageBytes(ctx, doc.URL, src)
				if int64(len(body)) > reserve {
					body = nil // larger than what was left for this page
				}
				var m webImageMeta
				if len(body) > 0 {
					m = decodeWebImageMeta(body)
				}
				mu.Lock()
				budget += reserve - int64(len(body))
				if len(body) > 0 {
					images[src] = body
				}
				fetched[src] = m
				mu.Unlock()
			}
		}()
	}
	for _, src := range remote {
		queue <- src
	}
	close(queue)
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
	browserChromeResource(req.Header, "image", browserAcceptImage, browserURL(pageURL), req.URL)
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
		Key:            b.ImageSrc,
		Rect:           sdl.Rect{X: x, Y: idx * rowH, W: w, H: h},
		ObjectFit:      b.Style.ObjectFit,
		ObjectPosition: b.Style.ObjectPosition,
		Invisible:      b.Style.Invisible,
		Opacity:        webPaintOpacity(b.Style),
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
		drawWebImageTexture(r, tex, rect, im.ObjectFit, im.ObjectPosition, im.Opacity)
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
	if err := drawWebImageTexture(r, tex, rect, im.ObjectFit, im.ObjectPosition, im.Opacity); err != nil {
		drawWebImagePlaceholder(r, config, rect)
	}
}

// drawWebImageTexture applies object-fit to one decoded texture. Fill is the
// reader's historic stretch; contain letterboxes inside the reserved box,
// cover crops the source centre, and none/scale-down preserve the natural
// pixel size when it fits.
func drawWebImageTexture(r *sdl.Renderer, tex *sdl.Texture, rect sdl.Rect, fit int, position string, opacity float64) error {
	if tex == nil || rect.W <= 0 || rect.H <= 0 {
		return nil
	}
	if opacity < 0.999 {
		_ = tex.SetAlphaMod(uint8(math.Round(opacity * 255)))
		defer tex.SetAlphaMod(255)
	}
	tw, th := int32(0), int32(0)
	if _, _, qw, qh, err := tex.Query(); err == nil {
		tw, th = qw, qh
	}
	if tw <= 0 || th <= 0 {
		return r.Copy(tex, nil, &rect)
	}
	posX, posY := webObjectPositionFraction(position, rect.W, rect.H)
	switch fit {
	case webObjectFitAuto, webObjectFitCover:
		copyWebImageCoverAt(r, tex, rect, posX, posY)
		return nil
	case webObjectFitFill:
		return r.Copy(tex, nil, &rect)
	case webObjectFitContain:
		dst := webObjectFitBoxAt(rect, tw, th, false, posX, posY)
		return r.Copy(tex, nil, &dst)
	case webObjectFitNone:
		dst := sdl.Rect{
			X: rect.X + int32(math.Round(float64(rect.W-tw)*posX)),
			Y: rect.Y + int32(math.Round(float64(rect.H-th)*posY)),
			W: tw, H: th,
		}
		return r.Copy(tex, nil, &dst)
	case webObjectFitScaleDown:
		if tw > rect.W || th > rect.H {
			dst := webObjectFitBoxAt(rect, tw, th, false, posX, posY)
			return r.Copy(tex, nil, &dst)
		}
		dst := sdl.Rect{
			X: rect.X + int32(math.Round(float64(rect.W-tw)*posX)),
			Y: rect.Y + int32(math.Round(float64(rect.H-th)*posY)),
			W: tw, H: th,
		}
		return r.Copy(tex, nil, &dst)
	default:
		copyWebImageCover(r, tex, rect)
		return nil
	}
}

// webObjectPositionFraction resolves the common object-position keywords,
// percentages and pixel lengths against the fitted box. The default is the
// centre, which is the previous behaviour.
func webObjectPositionFraction(value string, boxW, boxH int32) (float64, float64) {
	x, y := 0.5, 0.5
	fields := strings.Fields(strings.ReplaceAll(value, ",", " "))
	if len(fields) == 0 {
		return x, y
	}
	parse := func(token string, extent int32) (float64, bool, int) {
		switch strings.ToLower(strings.TrimSpace(token)) {
		case "left":
			return 0, true, 0
		case "right":
			return 1, true, 0
		case "top":
			return 0, true, 1
		case "bottom":
			return 1, true, 1
		case "center":
			return 0.5, true, -1
		}
		if pct, ok := webPercentValue(token); ok {
			return math.Max(0, math.Min(1, pct/100)), true, -1
		}
		if strings.HasSuffix(strings.ToLower(token), "px") {
			if extent <= 0 {
				extent = 1
			}
			if px := webLength(token, 16); px != 0 {
				return math.Max(0, math.Min(1, px/float64(extent))), true, -1
			}
		}
		return 0, false, -1
	}
	if len(fields) == 1 {
		f, ok, axis := parse(fields[0], boxW)
		if !ok {
			return x, y
		}
		if axis == 1 {
			y = f
		} else {
			x = f
		}
		return x, y
	}
	for i := 0; i < 2; i++ {
		extent := boxW
		if i == 1 {
			extent = boxH
		}
		f, ok, axis := parse(fields[i], extent)
		if !ok {
			continue
		}
		if axis == 0 {
			x = f
		} else if axis == 1 {
			y = f
		} else if i == 0 && x == 0.5 {
			x = f
		} else {
			y = f
		}
	}
	return x, y
}

// webObjectFitBox fits a source size inside a box for contain. The returned
// rectangle is centred, so a wide photo leaves equal bars above and below.
func webObjectFitBox(box sdl.Rect, tw, th int32, cover bool) sdl.Rect {
	return webObjectFitBoxAt(box, tw, th, cover, 0.5, 0.5)
}

func webObjectFitBoxAt(box sdl.Rect, tw, th int32, cover bool, posX, posY float64) sdl.Rect {
	if tw <= 0 || th <= 0 || box.W <= 0 || box.H <= 0 {
		return box
	}
	sx := float64(box.W) / float64(tw)
	sy := float64(box.H) / float64(th)
	scale := math.Min(sx, sy)
	if cover {
		scale = math.Max(sx, sy)
	}
	w := int32(math.Round(float64(tw) * scale))
	h := int32(math.Round(float64(th) * scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return sdl.Rect{
		X: box.X + int32(math.Round(float64(box.W-w)*posX)),
		Y: box.Y + int32(math.Round(float64(box.H-h)*posY)),
		W: w, H: h,
	}
}

// copyWebImageCoverAt blits one texture across an image/video card, cropping
// the source around object-position so a narrower source covers the target
// without letterboxing.
func copyWebImageCoverAt(r *sdl.Renderer, tex *sdl.Texture, rect sdl.Rect, posX, posY float64) {
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
			src = &sdl.Rect{X: int32(math.Round(float64(tw-sw) * posX)), Y: 0, W: sw, H: th}
		} else if srcAspect < targetAspect { // source too tall: crop top/bottom
			sh := int32(float64(tw)/targetAspect + 0.5)
			src = &sdl.Rect{X: 0, Y: int32(math.Round(float64(th-sh) * posY)), W: tw, H: sh}
		}
	}
	_ = r.Copy(tex, src, &rect)
}

func copyWebImageCover(r *sdl.Renderer, tex *sdl.Texture, rect sdl.Rect) {
	copyWebImageCoverAt(r, tex, rect, 0.5, 0.5)
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
