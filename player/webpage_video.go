package main

// webpage_video.go — videos in the reader.
//
// The styled reader skipped <video> and video-bearing <iframe> embeds as raw
// markup: a page with a clip was just words. This module turns those into
// content:
//
//   * the parser records every resolvable video as a dedicated block (from
//     the element's own src, a child <source>, or a known embed host);
//   * the layout reserves a 16:9 play card for it on the same uniform row
//     grid images use, and lists it among the page's links so the D-pad
//     reaches it exactly like an anchor;
//   * draw paints a dark card with a play glyph and the video's title, the
//     web-video stand-in for the play button the real page would have;
//   * activating it (tap, link cursor, link panel-style A press) never
//     navigates: the URL goes to playSmartURL, which streams media files
//     directly and hands everything else (YouTube, Vimeo, Twitter, ...)
//     to yt-dlp inside the embedded player.
//
// Nothing here fetches video bytes or metadata: the card is chrome, the
// playback path already exists, and an unresolvable src draws nothing at
// all rather than a dead box.

import (
	"math"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

// webVideoPos is one video in a laid-out page: the resolved URL and the
// rect reserved for its play card, in layout-local coordinates like Rects.
type webVideoPos struct {
	Src   string
	Title string
	// Thumb is the real thumbnail URL for this video — set only when it can
	// be derived without yt-dlp (YouTube pages by convention). "" keeps the
	// plain chrome-only card.
	Thumb          string
	Rect           sdl.Rect
	ObjectFit      int
	ObjectPosition string
	Invisible      bool
	Clip           sdl.Rect
	HasClip        bool
	Opacity        float64
}

// webSourceRe pulls the src of the first <source> child of a raw element.
var webSourceRe = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`(?i)<source[^>]*\bsrc\s*=\s*["']([^"']+)["']`) })

// webVideoHosts are the embed hosts yt-dlp supports that pages use for
// video, matched against the URL's host suffix (dot-boundary checked).
var webVideoHosts = []string{
	"youtube.com", "youtu.be", "youtube-nocookie.com", "vimeo.com",
	"dailymotion.com", "dai.ly", "twitch.tv",
}

// webVideoFileExts are media file extensions a raw src can end in, so an
// <iframe src=".../clip.mp4"> or a bare <video src> without a host still
// counts.
var webVideoFileExts = []string{".mp4", ".webm", ".m3u8", ".mkv", ".mov"}

// webVideoSrcFromElement works out which URL a video element points at:
// a <source> child first (the canonical HTML way), then the element's own
// src attribute or its lazy-loading data-src.
func webVideoSrcFromElement(attrs, inner string) string {
	if m := webSourceRe().FindStringSubmatch(inner); len(m) > 1 && m[1] != "" {
		return htmlAttrValue(m[1])
	}
	for _, name := range []string{"src", "data-src"} {
		if v := htmlTagAttribute(attrs, name); v != "" {
			return htmlAttrValue(v)
		}
	}
	return ""
}

// webVideoSrcFromIFrame only accepts an iframe whose destination is a video:
// embeds of YouTube/Vimeo/... or a direct media file. Everything else
// keeps the old behaviour (skip the markup entirely) so a structural
// iframe never renders as a play card.
func webVideoSrcFromIFrame(attrs, inner string) string {
	src := webVideoSrcFromElement(attrs, inner)
	if src == "" {
		return ""
	}
	lower := strings.ToLower(src)
	for _, ext := range webVideoFileExts {
		if strings.Contains(lower, ext) {
			return src
		}
	}
	parsed, err := url.Parse(src)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	for _, candidate := range webVideoHosts {
		if host == candidate || strings.HasSuffix(host, "."+candidate) {
			return src
		}
	}
	return ""
}

// webYouTubeThumbBase is the CDN video thumbnails come from. A package var
// so tests point it at a local server.
var webYouTubeThumbBase = "https://i.ytimg.com/vi/"

// webYouTubeVideoID pulls the video id out of the YouTube watch-page forms a
// web page embeds: /embed/<id>, /shorts/<id>, /live/<id>, /v/<id>, youtu.be/<id>
// or a watch?v=<id> query. Anything else (playlist views, channels, other
// hosts) returns "" and the card stays chrome-only.
var webYouTubeHosts = []string{"youtube.com", "youtu.be", "youtube-nocookie.com"}

func webYouTubeVideoID(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	isYouTube := false
	for _, candidate := range webYouTubeHosts {
		if host == candidate || strings.HasSuffix(host, "."+candidate) {
			isYouTube = true
			break
		}
	}
	if !isYouTube {
		return ""
	}
	id := parsed.Query().Get("v")
	if id == "" {
		segs := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		switch len(segs) {
		case 1: // youtu.be/<id>
			id = segs[0]
		case 2: // embed/<id>, shorts/<id>, live/<id>, v/<id> …
			id = segs[1]
		}
	}
	id = strings.TrimSpace(id)
	if len(id) != 11 {
		// A YouTube id is exactly 11 characters; a shorter token means the
		// URL is actually a playlist or channel path, not one video.
		return ""
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '-' || r == '_') {
			return ""
		}
	}
	return id
}

// webYouTubeThumbURL builds the thumbnail URL for one YouTube video, or "".
// mqdefault.jpg is 320x180 (16:9) and exists for every public video, unlike
// maxresdefault.jpg which 404s for many.
func webYouTubeThumbURL(raw string) string {
	id := webYouTubeVideoID(raw)
	if id == "" {
		return ""
	}
	return webYouTubeThumbBase + url.PathEscape(id) + "/mqdefault.jpg"
}

// addVideoBlock turns one raw <video> or <iframe> into a play-card block.
// An element without a resolvable URL is skipped entirely (no dead box);
// iframe embeds must point at a known video host or media file.
func (p *webParser) addVideoBlock(name, attrs, inner string) {
	if p.inHead {
		return
	}
	var src string
	switch name {
	case "video":
		src = webVideoSrcFromElement(attrs, inner)
	case "iframe":
		src = webVideoSrcFromIFrame(attrs, inner)
	default:
		return
	}
	resolved := p.webImageURL(src) // http(s) relative-to-document resolution
	if resolved == "" {
		return
	}
	title := htmlAttrValue(htmlTagAttribute(attrs, "title"))
	if title == "" {
		title = htmlAttrValue(htmlTagAttribute(attrs, "aria-label"))
	}
	title = strings.Join(strings.Fields(title), " ")
	p.flush()
	block := p.ensureBlock()
	videoStyle := p.computeStyle(webNewElement(name, attrs), attrs)
	block.Style = videoStyle
	block.Align = videoStyle.Align
	block.Indent = videoStyle.Indent
	block.Video = true
	block.VideoSrc = resolved
	block.ImageW = htmlTagAttributeFloat(attrs, "width")
	block.ImageH = htmlTagAttributeFloat(attrs, "height")
	if title != "" {
		block.VideoTitle = title
	}
	// YouTube embeds get their real thumbnail without yt-dlp: the video id
	// maps straight onto a URL on the image CDN, and the same thumbnail
	// pass that fetches <img> bytes picks it up.
	block.VideoThumb = webYouTubeThumbURL(resolved)
	p.flush()
}

// layoutVideo reserves a page layout's rows for one play card and records
// the box the painter draws into. It runs inside layoutBlocks' block loop,
// so margins, alignment and the webMaxRows cap behave exactly like images.
func (l *webLayouter) layoutVideo(b webBlock, padX, contentW int32) {
	layout, rowH := l.layout, l.rowH
	if rowH <= 0 || contentW <= 0 {
		return
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	w := contentW
	if b.Style.WidthPct > 0 {
		w = int32(float64(contentW) * b.Style.WidthPct / 100)
	} else if b.Style.Width > 0 {
		w = int32(b.Style.Width * scale)
		if b.Style.BoxSizing == 1 {
			w -= int32((b.Indent + b.Style.IndentRight) * scale)
		}
	} else if b.ImageW > 0 {
		w = int32(b.ImageW * scale)
	}
	if w < 0 {
		w = 0
	}
	if maxW := webConstraintLength(b.Style.MaxWidth, b.Style.MaxWidthPct, contentW, scale); maxW > 0 && float64(w)/scale > maxW {
		w = int32(math.Round(maxW * scale))
	}
	if minW := webConstraintLength(b.Style.MinWidth, b.Style.MinWidthPct, contentW, scale); minW > 0 && float64(w)/scale < minW {
		w = int32(math.Round(minW * scale))
	}
	if w > 0 {
		if w < webMinImagePx {
			w = webMinImagePx
		}
		if w > contentW {
			w = contentW
		}
	} else {
		w = contentW
	}
	if b.ImageW > 0 && b.Style.Width <= 0 && b.Style.WidthPct <= 0 && b.Style.MaxWidth <= 0 && b.Style.MaxWidthPct <= 0 && w < webMinImagePx {
		w = int32(b.ImageW * scale)
		if w < webMinImagePx {
			w = webMinImagePx
		}
	}
	h := w * 9 / 16 // 16:9, the ratio nearly every web video leans on
	if b.Style.Height > 0 {
		h = int32(b.Style.Height * scale)
	}
	if maxH := b.Style.MaxHeight * scale; maxH > 0 && float64(h) > maxH {
		h = int32(math.Round(maxH))
	}
	if minH := b.Style.MinHeight * scale; minH > 0 && float64(h) < minH {
		h = int32(math.Round(minH))
	}
	if h < rowH {
		h = rowH
	}
	if h > contentW*3/4 {
		h = contentW * 3 / 4
	}
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
	layout.Videos = append(layout.Videos, webVideoPos{
		Src:            b.VideoSrc,
		Title:          b.VideoTitle,
		Thumb:          b.VideoThumb,
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

// collectWebVideoLinks turns every laid-out play card into a link hit the
// browser walks and activates like an anchor, with Video set so activation
// plays instead of navigating. Videos sit between the text links by row so
// the D-pad order matches the page order.
func collectWebVideoLinks(layout *webLayout) []webLinkHit {
	if layout == nil || layout.RowH <= 0 || len(layout.Videos) == 0 {
		return nil
	}
	out := make([]webLinkHit, 0, len(layout.Videos))
	for _, v := range layout.Videos {
		title := strings.TrimSpace(v.Title)
		if title == "" {
			title = "Play video"
		}
		out = append(out, webLinkHit{
			Row:   int(v.Rect.Y / layout.RowH),
			X:     v.Rect.X,
			W:     v.Rect.W,
			Href:  v.Src,
			Text:  "▶ " + title,
			Video: true,
		})
	}
	return out
}

// drawWebVideo paints one laid-out play card. When the thumbnail pass caught
// the video's real artwork (YouTube embeds carry their image CDNs URL-derived
// artwork for free), it covers the card center-cropped, with the play glyph
// and title drawn on top; anything else falls back to the dark chrome card
// the media browser uses. Nothing here touches the network.
func drawWebVideo(r *sdl.Renderer, config *Config, doc *webDocument, v webVideoPos, x, y int32) {
	rect := sdl.Rect{X: x, Y: y, W: v.Rect.W, H: v.Rect.H}
	if r == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}
	opacity := webVideoPaintOpacity(v)
	if !drawWebVideoThumb(r, doc, v.Thumb, rect, v.ObjectFit, v.ObjectPosition, opacity) {
		fillRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 4, webScaleColorAlpha(sdl.Color{R: 16, G: 18, B: 24, A: 255}, opacity))
	}
	strokeRoundedRect(r, rect.X, rect.Y, rect.W, rect.H, 4, 1, webScaleColorAlpha(WithAlpha(accentColor, 110), opacity))

	// A centered play glyph: the card is tappable, so the affordance is
	// drawn where the finger already is.
	cx := rect.X + rect.W/2
	cy := rect.Y + rect.H/2
	glyph := int32(34)
	if rect.H < glyph*3 {
		glyph = rect.H / 3
	}
	if glyph < 10 {
		glyph = 10
	}
	at := func(fx, fy float64) pt {
		return pt{
			x: cx - glyph/2 + int32(fx*float64(glyph)),
			y: cy - glyph/2 + int32(fy*float64(glyph)),
		}
	}
	fillTriangleFilled(r, at(0.10, 0.02), at(0.10, 0.98), at(0.94, 0.50),
		webScaleColorAlpha(sdl.Color{R: 250, G: 250, B: 250, A: 255}, opacity))

	// The title runs along the card's top-left, clipped to fit, with the
	// small font the rest of the chrome uses.
	title := strings.TrimSpace(v.Title)
	if title == "" || config == nil {
		return
	}
	font := getSmallFont(config)
	if font == nil {
		font = getBodyFont(config, 14)
	}
	if font == nil {
		return
	}
	const pad = int32(10)
	label := ellipsize(font, title, rect.W-pad*2)
	if label == "" {
		return
	}
	// Height is ignored: the row's baseline alignment accounts for it.
	renderText(r, config, font, label, webScaleColorAlpha(ColorTextSecondary(), opacity), rect.X+pad, rect.Y+pad)
}

func webVideoPaintOpacity(v webVideoPos) float64 {
	return webClampOpacity(v.Opacity)
}

// drawWebVideoThumb blits the video's thumbnail art over its play card,
// return true when it did. Bytes come from the document (the same image pass
// that fetched <img>s), decode once through the shared texture cache, and a
// narrow image is cover-cropped so the card never letterboxes a hole in it.
func drawWebVideoThumb(r *sdl.Renderer, doc *webDocument, thumb string, rect sdl.Rect, fit int, position string, opacity float64) bool {
	if r == nil || doc == nil || thumb == "" {
		return false
	}
	key := webImageCacheKey(thumb)
	if tex := getThumbnailTextures().Get(key); tex != nil {
		_ = drawWebImageTexture(r, tex, rect, fit, position, opacity)
		return true
	}
	data := doc.imageBytes(thumb)
	if len(data) == 0 {
		return false
	}
	rwops, err := sdl.RWFromMem(data)
	if err != nil || rwops == nil {
		return false
	}
	tex, err := img.LoadTextureRW(r, rwops, false)
	rwops.Close()
	if err != nil || tex == nil {
		return false
	}
	tw, th := int32(0), int32(0)
	if _, _, qw, qh, qerr := tex.Query(); qerr == nil {
		tw, th = qw, qh
	}
	getThumbnailTextures().Put(key, tex, tw, th)
	_ = drawWebImageTexture(r, tex, rect, fit, position, opacity)
	return true
}
