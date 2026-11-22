package main

// webpage_svg.go — SVG in the styled web reader.
//
// The reader kept an <svg> as an empty placeholder box and dropped SVG images
// altogether, so every icon, logo and social button on a modern page was a
// hole. The build has no SVG library and the handheld has no browser engine
// to borrow one from, so this file carries a small one:
//
//   - a tolerant parser (encoding/xml in its HTML mode) for inline <svg>
//     elements, SVG files and data:image/svg+xml URIs;
//   - what icons and logos are made of: paths (every command, arcs included),
//     the basic shapes, groups and transforms, <use> and <symbol> (sprite
//     sheets elsewhere on the page too), presentation attributes, style="",
//     simple <style> rules, currentColor, the opacities, and linear and
//     radial gradients;
//   - an anti-aliased scanline rasteriser (nonzero and evenodd) with a stroker
//     for caps and joins, writing a PNG the image path already knows how to
//     draw.
//
// Text, embedded images, filters, masks, clip paths, patterns, markers and
// dashes are skipped; the shapes around them still draw. Everything is
// bounded — markup size, element count, nesting, <use> depth, edge count and
// raster size — so a hostile document costs milliseconds, not the handheld.

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// webSVGInlinePrefix starts the image key of an inline <svg>: the image
	// pass rasterises those locally and never sends them to the network.
	webSVGInlinePrefix = "jhsvg:"
	// webSVGMaxMarkup bounds one SVG document.
	webSVGMaxMarkup = 2 << 20
	// webSVGMaxInline and webSVGMaxInlineBytes bound the distinct inline
	// icons one page keeps for the image pass.
	webSVGMaxInline      = 512
	webSVGMaxInlineBytes = 8 << 20
	// webSVGMaxDefBytes bounds the sprite sheets kept for <use>.
	webSVGMaxDefBytes = 2 << 20
	webSVGMaxNodes    = 20000
	webSVGMaxDepth    = 64
	webSVGMaxUseDepth = 8
	// webSVGMaxEdges bounds the flattened edges one document may produce.
	webSVGMaxEdges = 200000
	// webSVGMaxRaster is the longest side of the rasterised picture.
	webSVGMaxRaster = 1024
	// webSVGRasterScale is raster pixels per CSS px: the reader shows a CSS
	// px as about 1.5 screen pixels, so 2 keeps edges crisp after scaling.
	webSVGRasterScale = 2.0
	// webSVGSubScanlines is the vertical anti-aliasing: coverage is sampled
	// on this many lines per pixel row, and exactly along each line.
	webSVGSubScanlines = 4
)

// webSVGSource is one inline <svg> waiting for the image pass: its markup and
// what the page's CSS said about it.
type webSVGSource struct {
	Markup string
	// Color is the element's text colour, which currentColor means.
	Color sdl.Color
	// Fill and Stroke are the page CSS's fill/stroke on the element ("" when
	// none), which win over the root's own presentation attributes.
	Fill   string
	Stroke string
	// W and H are the element's CSS width and height when the page gave them
	// (0 otherwise): the raster is drawn for that size rather than for a
	// 512-unit viewBox shown at 16px.
	W, H float64
}

// webSVGOptions is what a rasterisation needs besides the markup.
type webSVGOptions struct {
	Color    sdl.Color
	HasColor bool
	Fill     string
	Stroke   string
	W, H     float64
	// Defs are the page's parsed sprite sheets, searched when a <use> names
	// an id the document itself does not have.
	Defs []*webSVGDoc
}

// --- Parser side: inline <svg> -------------------------------------------------

// webSVGPaintDecl is the cascaded value of the CSS fill/stroke property: the
// raw text (currentColor stays symbolic), with the CSS-wide keywords applied.
func webSVGPaintDecl(value, inherited string) string {
	v := strings.TrimSpace(value)
	switch strings.ToLower(v) {
	case "inherit", "unset":
		return inherited
	case "initial", "revert", "revert-layer":
		return ""
	}
	return v
}

// webSVGElementEnd finds the </svg> that closes the element whose body starts
// at start, counting nested <svg> elements: an icon that wraps another <svg>
// used to end at the inner close tag and leak the rest as page text. bodyEnd is
// the offset of the matching close tag and next the offset just past it.
func webSVGElementEnd(source string, start int) (bodyEnd, next int, ok bool) {
	if start < 0 || start > len(source) {
		return 0, 0, false
	}
	depth := 1
	for i := start; i < len(source); {
		j := strings.IndexByte(source[i:], '<')
		if j < 0 {
			break
		}
		i += j
		rest := source[i:]
		if strings.HasPrefix(rest, "<!--") {
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		if strings.HasPrefix(rest, "<![CDATA[") {
			end := strings.Index(rest, "]]>")
			if end < 0 {
				break
			}
			i += end + 3
			continue
		}
		nameAt := i + 1
		closing := nameAt < len(source) && source[nameAt] == '/'
		if closing {
			nameAt++
		}
		if !webSVGTagNamed(source, nameAt, "svg") {
			i++
			continue
		}
		gt := findTextBrowserTagEnd(source, i+1)
		if gt < 0 {
			break
		}
		if closing {
			depth--
			if depth == 0 {
				return i, gt + 1, true
			}
		} else if source[gt-1] != '/' {
			depth++
		}
		i = gt + 1
	}
	return 0, 0, false
}

// webSVGTagNamed reports whether the tag name at source[at:] is name, in any
// letter case and followed by the end of the name.
func webSVGTagNamed(source string, at int, name string) bool {
	end := at + len(name)
	if at < 0 || end > len(source) || !strings.EqualFold(source[at:end], name) {
		return false
	}
	if end == len(source) {
		return true
	}
	switch source[end] {
	case ' ', '\t', '\r', '\n', '\f', '/', '>':
		return true
	}
	return false
}

// walkSVG takes an inline <svg> whose start tag ends at bodyStart, and returns
// the offset just past the element. Its sprite definitions are kept for <use>
// elsewhere on the page even when the element itself is hidden (sprite sheets
// sit in display:none wrappers); a visible one with something to draw becomes
// an image the image pass rasterises.
func (p *webParser) walkSVG(source string, tagStart, bodyStart int, attrs string, selfClose bool) int {
	next := bodyStart
	if !selfClose {
		_, next, _ = webRawElement(source, bodyStart, "svg")
	}
	if tagStart < 0 || tagStart >= next || next > len(source) {
		return next
	}
	markup := source[tagStart:next]
	if len(markup) > webSVGMaxMarkup {
		return next
	}
	p.registerSVGDefs(markup)
	if p.inHead || p.topStyle().Hidden || p.textarea != nil || p.selectCtrl != nil {
		return next
	}
	style := p.computeStyle(webNewElement("svg", attrs), attrs)
	if style.Hidden {
		return next
	}
	if !webSVGHasDrawable(markup) {
		// Nothing to draw: a sprite sheet or a gradient holder takes no room,
		// while an empty <svg> the page gave a background or border keeps
		// its painted box, as it always has.
		if webStyleHasBox(style) {
			p.addPlaceholderBlock("svg", attrs)
		}
		return next
	}
	attrW, okW := webSVGAttrLength(htmlAttrValue(htmlTagAttribute(attrs, "width")), style.FontPx)
	attrH, okH := webSVGAttrLength(htmlAttrValue(htmlTagAttribute(attrs, "height")), style.FontPx)
	if (okW && attrW <= 0) || (okH && attrH <= 0) {
		// width="0" or height="0": a gradient holder or a sprite sheet.
		return next
	}
	// A missing size comes from the viewBox, the way the intrinsic size of an
	// <img src="icon.svg"> would: an unsized 24-unit icon stays icon-sized.
	if vb, ok := webSVGViewBox(htmlAttrValue(htmlTagAttribute(attrs, "viewbox"))); ok {
		switch {
		case attrW <= 0 && attrH <= 0:
			attrW, attrH = vb[2], vb[3]
		case attrW <= 0:
			attrW = attrH * vb[2] / vb[3]
		case attrH <= 0:
			attrH = attrW * vb[3] / vb[2]
		}
	}
	color := webResolveColor(style, p.doc)
	cssW, cssH := 0.0, 0.0
	if style.Width > 0 {
		cssW = style.Width
	}
	if style.Height > 0 {
		cssH = style.Height
	}
	key := webSVGKey(markup, color, style.SVGFill, style.SVGStroke, cssW, cssH)
	if p.doc.SVGInline == nil {
		p.doc.SVGInline = make(map[string]webSVGSource)
	}
	if _, ok := p.doc.SVGInline[key]; !ok {
		if len(p.doc.SVGInline) >= webSVGMaxInline || p.doc.SVGInlineBytes+len(markup) > webSVGMaxInlineBytes {
			return next
		}
		p.doc.SVGInline[key] = webSVGSource{
			Markup: markup, Color: color, Fill: style.SVGFill, Stroke: style.SVGStroke, W: cssW, H: cssH,
		}
		p.doc.SVGInlineBytes += len(markup)
	}
	p.addImageElement(style, key, htmlAttrValue(htmlTagAttribute(attrs, "aria-label")), attrW, attrH)
	return next
}

// registerSVGDefs keeps an <svg> that defines symbols or other definitions, so
// a <use href="#icon"> in a later <svg> finds them.
func (p *webParser) registerSVGDefs(markup string) {
	if !strings.Contains(markup, "<symbol") && !strings.Contains(markup, "<defs") {
		return
	}
	if p.doc.SVGDefBytes+len(markup) > webSVGMaxDefBytes {
		return
	}
	p.doc.SVGDefs = append(p.doc.SVGDefs, markup)
	p.doc.SVGDefBytes += len(markup)
}

// webSVGKey names one inline <svg> in the image maps. The colour and CSS paint
// are part of it: the same icon beside white and beside dark text is two
// pictures.
func webSVGKey(markup string, color sdl.Color, fill, stroke string, w, h float64) string {
	sum := sha1.New()
	_, _ = io.WriteString(sum, markup)
	_, _ = fmt.Fprintf(sum, "\x00%02x%02x%02x%02x\x00%s\x00%s\x00%g\x00%g", color.R, color.G, color.B, color.A, fill, stroke, w, h)
	return webSVGInlinePrefix + hex.EncodeToString(sum.Sum(nil))
}

// webSVGHasDrawable reports whether markup has a shape outside the containers
// that are never drawn directly (defs, symbol, gradients, clip paths, …). A
// sprite sheet or a gradient holder must not reserve an empty box.
func webSVGHasDrawable(markup string) bool {
	hidden := 0
	for i := 0; i < len(markup); {
		j := strings.IndexByte(markup[i:], '<')
		if j < 0 {
			break
		}
		i += j
		if strings.HasPrefix(markup[i:], "<!--") {
			end := strings.Index(markup[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		end := strings.IndexByte(markup[i:], '>')
		if end < 0 {
			break
		}
		tag := markup[i+1 : i+end]
		i += end + 1
		closing := strings.HasPrefix(tag, "/")
		tag = strings.TrimPrefix(tag, "/")
		nameEnd := 0
		for nameEnd < len(tag) && tag[nameEnd] != ' ' && tag[nameEnd] != '\t' && tag[nameEnd] != '\r' &&
			tag[nameEnd] != '\n' && tag[nameEnd] != '/' {
			nameEnd++
		}
		name := strings.ToLower(tag[:nameEnd])
		if k := strings.IndexByte(name, ':'); k >= 0 {
			name = name[k+1:]
		}
		selfClose := strings.HasSuffix(tag, "/")
		switch name {
		case "defs", "symbol", "clippath", "mask", "pattern", "marker", "lineargradient", "radialgradient",
			"filter", "style", "title", "desc", "metadata", "script":
			if closing {
				if hidden > 0 {
					hidden--
				}
			} else if !selfClose {
				hidden++
			}
		case "path", "rect", "circle", "ellipse", "line", "polyline", "polygon", "use":
			if !closing && hidden == 0 {
				return true
			}
		}
	}
	return false
}

// webSVGAttrLength reads an absolute length (a number with an optional CSS
// unit). ok is false for percentages, auto and anything unreadable.
func webSVGAttrLength(value string, fontPx float64) (float64, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "auto" || strings.HasSuffix(v, "%") {
		return 0, false
	}
	i := len(v)
	for i > 0 && v[i-1] >= 'a' && v[i-1] <= 'z' {
		i--
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v[:i]), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if fontPx <= 0 {
		fontPx = 16
	}
	switch v[i:] {
	case "", "px":
	case "pt":
		f *= 96.0 / 72
	case "pc":
		f *= 16
	case "in":
		f *= 96
	case "cm":
		f *= 96 / 2.54
	case "mm":
		f *= 96 / 25.4
	case "q":
		f *= 96 / 101.6
	case "em":
		f *= fontPx
	case "rem":
		f *= 16
	case "ex", "ch":
		f *= fontPx / 2
	default:
		return 0, false
	}
	return f, true
}

// webSVGViewBox reads "min-x min-y width height".
func webSVGViewBox(value string) ([4]float64, bool) {
	nums := webSVGNumbers(value)
	if len(nums) != 4 || nums[2] <= 0 || nums[3] <= 0 {
		return [4]float64{}, false
	}
	return [4]float64{nums[0], nums[1], nums[2], nums[3]}, true
}

// --- Image pipeline side ---------------------------------------------------------

// webIsSVGDataURI reports a data:image/svg+xml URI, whatever its encoding.
func webIsSVGDataURI(src string) bool {
	const prefix = "data:image/svg+xml"
	return len(src) >= len(prefix) && strings.EqualFold(src[:len(prefix)], prefix)
}

// webSVGDataURIBytes decodes the markup of an SVG data URI: base64, or the
// percent-encoded (or raw ";utf8,") text CSS backgrounds usually carry.
func webSVGDataURIBytes(src string) ([]byte, bool) {
	comma := strings.IndexByte(src, ',')
	if comma < 0 {
		return nil, false
	}
	header := strings.ToLower(src[:comma])
	payload := src[comma+1:]
	if payload == "" || len(payload) > 3*webSVGMaxMarkup {
		return nil, false
	}
	if strings.Contains(header, ";base64") {
		payload = strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\r', '\n':
				return -1
			}
			return r
		}, payload)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if raw, err := enc.DecodeString(payload); err == nil && len(raw) > 0 {
				return raw, true
			}
		}
		return nil, false
	}
	if decoded, err := url.PathUnescape(payload); err == nil {
		return []byte(decoded), true
	}
	return []byte(webSVGUnescapeLenient(payload)), true
}

// webSVGUnescapeLenient decodes the valid %XX escapes and keeps everything
// else, for a URI whose stray '%' makes the strict decoder give up.
func webSVGUnescapeLenient(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := webSVGHexValue(s[i+1])
			lo, ok2 := webSVGHexValue(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func webSVGHexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// webSVGLooksLike sniffs a fetched body: servers label SVG as image/svg+xml,
// text/plain or application/octet-stream alike, so the bytes decide.
func webSVGLooksLike(body []byte) bool {
	head := body
	if len(head) > 4096 {
		head = head[:4096]
	}
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	head = bytes.TrimLeft(head, " \t\r\n")
	if len(head) == 0 || head[0] != '<' {
		return false
	}
	s := string(head)
	at := webIndexFold(s, "<svg")
	if at < 0 {
		return false
	}
	if h := webIndexFold(s, "<html"); h >= 0 && h < at {
		return false
	}
	return true
}

// webSVGParseDefs parses the page's sprite sheets once for a whole image pass.
func webSVGParseDefs(defs []string) []*webSVGDoc {
	var out []*webSVGDoc
	for _, markup := range defs {
		if doc := webSVGParseDoc([]byte(markup)); doc != nil && len(doc.ids) > 0 {
			out = append(out, doc)
		}
	}
	return out
}

// rasterizeInlineSVG draws the inline <svg> the parser kept under key.
func (d *webDocument) rasterizeInlineSVG(key string, defs []*webSVGDoc) ([]byte, webImageMeta, bool) {
	if d == nil {
		return nil, webImageMeta{}, false
	}
	src, ok := d.SVGInline[key]
	if !ok {
		return nil, webImageMeta{}, false
	}
	return webRasterizeSVG([]byte(src.Markup), webSVGOptions{
		Color: src.Color, HasColor: true, Fill: src.Fill, Stroke: src.Stroke,
		W: src.W, H: src.H, Defs: defs,
	})
}

// --- Document model ---------------------------------------------------------------

// webSVGNode is one parsed element. Attribute names are lower case and without
// a namespace prefix (xlink:href is href).
type webSVGNode struct {
	name     string
	attrs    map[string]string
	children []*webSVGNode
	doc      *webSVGDoc
}

// webSVGDoc is one parsed SVG document: its root, its ids and its <style> rules.
type webSVGDoc struct {
	root  *webSVGNode
	ids   map[string]*webSVGNode
	rules []webSVGRule
}

// webSVGParseDoc reads markup into a tree, tolerant of HTML habits (unquoted
// and bare attributes, entities, unclosed elements). A syntax error ends the
// parse but keeps what came before it.
func webSVGParseDoc(data []byte) *webSVGDoc {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	doc := &webSVGDoc{ids: map[string]*webSVGNode{}}
	top := &webSVGNode{name: "#document", doc: doc}
	stack := []*webSVGNode{top}
	skip, nodes := 0, 0
	var css strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 || len(stack) > webSVGMaxDepth || nodes >= webSVGMaxNodes {
				skip++
				continue
			}
			nodes++
			n := &webSVGNode{name: strings.ToLower(t.Name.Local), doc: doc}
			if len(t.Attr) > 0 {
				n.attrs = make(map[string]string, len(t.Attr))
				for _, a := range t.Attr {
					key := strings.ToLower(a.Name.Local)
					if key == "xmlns" || a.Name.Space == "xmlns" {
						continue
					}
					n.attrs[key] = a.Value
				}
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, n)
			if id := strings.TrimSpace(n.attrs["id"]); id != "" {
				if _, dup := doc.ids[id]; !dup {
					doc.ids[id] = n
				}
			}
			stack = append(stack, n)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if skip == 0 && len(stack) > 1 && stack[len(stack)-1].name == "style" && css.Len() < 256<<10 {
				css.Write(t)
				css.WriteByte('\n')
			}
		}
	}
	for _, n := range top.children {
		if n.name == "svg" {
			doc.root = n
			break
		}
	}
	if css.Len() > 0 {
		doc.rules = webSVGParseRules(css.String())
	}
	return doc
}

// webSVGRule is one rule of an SVG <style> sheet: exported logos style their
// shapes with classes (.cls-1{fill:#e94e1b}).
type webSVGRule struct {
	sels  []webSVGSelector
	decls map[string]string
}

// webSVGSelector is a compound selector of a tag, an id and classes. Only the
// last compound of a complex selector is kept, and pseudo-classes and
// attribute selectors are not modelled (such a selector never matches).
type webSVGSelector struct {
	tag     string
	id      string
	classes []string
}

func webSVGParseRules(css string) []webSVGRule {
	css = stripWebCSSComments(css)
	var rules []webSVGRule
	for len(rules) < 2000 {
		open := strings.IndexByte(css, '{')
		if open < 0 {
			break
		}
		close := strings.IndexByte(css[open:], '}')
		if close < 0 {
			break
		}
		close += open
		prelude := strings.TrimSpace(css[:open])
		body := css[open+1 : close]
		css = css[close+1:]
		if prelude == "" || strings.HasPrefix(prelude, "@") {
			continue
		}
		var rule webSVGRule
		for _, part := range strings.Split(prelude, ",") {
			if sel, ok := webSVGParseSelector(part); ok {
				rule.sels = append(rule.sels, sel)
			}
		}
		if len(rule.sels) == 0 {
			continue
		}
		decls, important := parseWebDeclsSplit(body)
		for k, v := range important {
			decls[k] = v
		}
		rule.decls = decls
		rules = append(rules, rule)
	}
	return rules
}

func webSVGParseSelector(s string) (webSVGSelector, bool) {
	s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "}"))
	if s == "" || strings.ContainsAny(s, ":[@{") {
		return webSVGSelector{}, false
	}
	if i := strings.LastIndexAny(s, " \t\r\n>+~"); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return webSVGSelector{}, false
	}
	var sel webSVGSelector
	i := 0
	ident := func() string {
		start := i
		for i < len(s) && s[i] != '.' && s[i] != '#' {
			i++
		}
		return s[start:i]
	}
	if s[0] != '.' && s[0] != '#' {
		sel.tag = strings.ToLower(ident())
		if sel.tag == "*" {
			sel.tag = ""
		}
	}
	for i < len(s) {
		kind := s[i]
		i++
		name := ident()
		if name == "" {
			return webSVGSelector{}, false
		}
		if kind == '.' {
			sel.classes = append(sel.classes, name)
		} else {
			sel.id = name
		}
	}
	return sel, true
}

func (sel webSVGSelector) matches(n *webSVGNode) bool {
	if sel.tag != "" && sel.tag != n.name {
		return false
	}
	if sel.id != "" && strings.TrimSpace(n.attrs["id"]) != sel.id {
		return false
	}
	if len(sel.classes) > 0 {
		classes := strings.Fields(n.attrs["class"])
		for _, want := range sel.classes {
			found := false
			for _, c := range classes {
				if c == want {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

// webSVGPresentation lists the presentation attributes the renderer reads.
var webSVGPresentation = [...]string{
	"fill", "stroke", "stroke-width", "opacity", "fill-opacity", "stroke-opacity", "fill-rule",
	"stroke-linecap", "stroke-linejoin", "stroke-miterlimit", "display", "visibility", "color",
	"stop-color", "stop-opacity",
}

// webSVGDeclared is an element's own declarations in cascade order:
// presentation attributes, then <style> rules (later rules win), then its
// style attribute.
func webSVGDeclared(n *webSVGNode) map[string]string {
	props := make(map[string]string, 8)
	for _, name := range webSVGPresentation {
		if v, ok := n.attrs[name]; ok {
			props[name] = strings.TrimSpace(v)
		}
	}
	if n.doc != nil {
		for _, rule := range n.doc.rules {
			for _, sel := range rule.sels {
				if sel.matches(n) {
					for k, v := range rule.decls {
						props[k] = v
					}
					break
				}
			}
		}
	}
	if style := n.attrs["style"]; style != "" {
		decls, important := parseWebDeclsSplit(style)
		for k, v := range decls {
			props[k] = v
		}
		for k, v := range important {
			props[k] = v
		}
	}
	return props
}

// --- Paint state -------------------------------------------------------------------

const (
	webSVGPaintNone = iota
	webSVGPaintColor
	webSVGPaintCurrent
	webSVGPaintURL
)

// webSVGPaint is a fill or stroke: none, a colour, currentColor (resolved
// against the painting element's own colour) or a gradient reference with an
// optional fallback.
type webSVGPaint struct {
	kind    uint8
	color   sdl.Color
	ref     string
	fbKind  uint8
	fbColor sdl.Color
}

// webSVGState is the inherited paint state while walking the tree.
type webSVGState struct {
	fill, stroke  webSVGPaint
	fillOpacity   float64
	strokeOpacity float64
	// opacity is the product of the group opacities above: the reader
	// paints each shape with it instead of compositing the group.
	opacity     float64
	strokeWidth float64
	evenOdd     bool
	lineCap     uint8 // 0 butt, 1 round, 2 square
	lineJoin    uint8 // 0 miter, 1 round, 2 bevel
	miterLimit  float64
	color       sdl.Color
	visible     bool
	// vw and vh are the current viewport in user units, for percentages.
	vw, vh float64
}

func (st webSVGState) diag() float64 {
	return math.Sqrt((st.vw*st.vw + st.vh*st.vh) / 2)
}

func webSVGParsePaint(value string, cur webSVGPaint) (webSVGPaint, bool) {
	v := strings.TrimSpace(value)
	lower := strings.ToLower(v)
	switch lower {
	case "":
		return cur, false
	case "inherit":
		return cur, true
	case "none", "transparent":
		return webSVGPaint{kind: webSVGPaintNone}, true
	case "currentcolor":
		return webSVGPaint{kind: webSVGPaintCurrent}, true
	}
	if strings.HasPrefix(lower, "url(") {
		end := strings.IndexByte(v, ')')
		if end < 0 {
			return cur, false
		}
		ref := strings.Trim(strings.TrimSpace(v[4:end]), `"'`)
		if i := strings.IndexByte(ref, '#'); i >= 0 {
			ref = ref[i+1:]
		}
		p := webSVGPaint{kind: webSVGPaintURL, ref: ref}
		if rest := strings.TrimSpace(v[end+1:]); rest != "" {
			if fb, ok := webSVGParsePaint(rest, webSVGPaint{}); ok && fb.kind != webSVGPaintURL {
				p.fbKind, p.fbColor = fb.kind, fb.color
			}
		}
		return p, true
	}
	if c, ok := parseWebColor(v); ok {
		return webSVGPaint{kind: webSVGPaintColor, color: c}, true
	}
	return cur, false
}

// webSVGOpacity reads a number or a percentage, clamped to 0..1.
func webSVGOpacity(value string, def float64) float64 {
	v := strings.TrimSpace(value)
	pct := strings.HasSuffix(v, "%")
	f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
	if err != nil || math.IsNaN(f) {
		return def
	}
	if pct {
		f /= 100
	}
	return math.Max(0, math.Min(1, f))
}

// webSVGApply folds one element's declarations into the inherited state.
func webSVGApply(st webSVGState, props map[string]string) webSVGState {
	if v, ok := props["color"]; ok {
		if c, valid := parseWebColor(v); valid {
			st.color = c
		}
	}
	if v, ok := props["fill"]; ok {
		if p, valid := webSVGParsePaint(v, st.fill); valid {
			st.fill = p
		}
	}
	if v, ok := props["stroke"]; ok {
		if p, valid := webSVGParsePaint(v, st.stroke); valid {
			st.stroke = p
		}
	}
	if v, ok := props["fill-opacity"]; ok {
		st.fillOpacity = webSVGOpacity(v, st.fillOpacity)
	}
	if v, ok := props["stroke-opacity"]; ok {
		st.strokeOpacity = webSVGOpacity(v, st.strokeOpacity)
	}
	if v, ok := props["opacity"]; ok {
		st.opacity *= webSVGOpacity(v, 1)
	}
	if v, ok := props["stroke-width"]; ok {
		if w, valid := webSVGLength(v, st.diag()); valid && w >= 0 {
			st.strokeWidth = w
		}
	}
	if v, ok := props["fill-rule"]; ok {
		st.evenOdd = strings.EqualFold(strings.TrimSpace(v), "evenodd")
	}
	if v, ok := props["stroke-linecap"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "butt":
			st.lineCap = 0
		case "round":
			st.lineCap = 1
		case "square":
			st.lineCap = 2
		}
	}
	if v, ok := props["stroke-linejoin"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "miter", "miter-clip", "arcs":
			st.lineJoin = 0
		case "round":
			st.lineJoin = 1
		case "bevel":
			st.lineJoin = 2
		}
	}
	if v, ok := props["stroke-miterlimit"]; ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f >= 1 {
			st.miterLimit = f
		}
	}
	if v, ok := props["visibility"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "hidden", "collapse":
			st.visible = false
		case "visible":
			st.visible = true
		}
	}
	return st
}

// webSVGLength resolves a coordinate or length in user units; a percentage is
// of ref (the viewport's width, height or normalised diagonal).
func webSVGLength(value string, ref float64) (float64, bool) {
	v := strings.TrimSpace(value)
	if strings.HasSuffix(v, "%") {
		f, err := strconv.ParseFloat(strings.TrimSpace(v[:len(v)-1]), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f * ref / 100, true
	}
	return webSVGAttrLength(v, 16)
}

// webSVGFraction reads a gradient coordinate in bounding-box units: a number,
// or a percentage of the box.
func webSVGFraction(value string) (float64, bool) {
	v := strings.TrimSpace(value)
	pct := strings.HasSuffix(v, "%")
	f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	if pct {
		f /= 100
	}
	return f, true
}

// --- Geometry ----------------------------------------------------------------------

// webSVGMatrix is an affine transform [a c e; b d f].
type webSVGMatrix struct{ a, b, c, d, e, f float64 }

var webSVGIdentity = webSVGMatrix{a: 1, d: 1}

// mul is m×n: n applies first.
func (m webSVGMatrix) mul(n webSVGMatrix) webSVGMatrix {
	return webSVGMatrix{
		a: m.a*n.a + m.c*n.b,
		b: m.b*n.a + m.d*n.b,
		c: m.a*n.c + m.c*n.d,
		d: m.b*n.c + m.d*n.d,
		e: m.a*n.e + m.c*n.f + m.e,
		f: m.b*n.e + m.d*n.f + m.f,
	}
}

func (m webSVGMatrix) apply(x, y float64) (float64, float64) {
	return m.a*x + m.c*y + m.e, m.b*x + m.d*y + m.f
}

func (m webSVGMatrix) invert() (webSVGMatrix, bool) {
	det := m.a*m.d - m.b*m.c
	if math.Abs(det) < 1e-12 || math.IsNaN(det) {
		return webSVGIdentity, false
	}
	inv := 1 / det
	return webSVGMatrix{
		a: m.d * inv, b: -m.b * inv,
		c: -m.c * inv, d: m.a * inv,
		e: (m.c*m.f - m.d*m.e) * inv,
		f: (m.b*m.e - m.a*m.f) * inv,
	}, true
}

// scale is the transform's mean scale factor, for stroke widths and the
// flattening density.
func (m webSVGMatrix) scale() float64 {
	return math.Sqrt(math.Abs(m.a*m.d - m.b*m.c))
}

func webSVGTranslate(x, y float64) webSVGMatrix { return webSVGMatrix{a: 1, d: 1, e: x, f: y} }

func webSVGScale(x, y float64) webSVGMatrix { return webSVGMatrix{a: x, d: y} }

// webSVGParseTransform reads a transform list; an unknown or malformed
// function is skipped rather than voiding the list.
func webSVGParseTransform(value string) webSVGMatrix {
	m := webSVGIdentity
	s := value
	for guard := 0; guard < 64; guard++ {
		s = strings.TrimLeft(s, " \t\r\n,")
		open := strings.IndexByte(s, '(')
		if open < 0 {
			break
		}
		closeAt := strings.IndexByte(s[open:], ')')
		if closeAt < 0 {
			break
		}
		closeAt += open
		name := strings.ToLower(strings.TrimSpace(s[:open]))
		args := webSVGNumbers(s[open+1 : closeAt])
		s = s[closeAt+1:]
		if len(args) == 0 {
			continue
		}
		var t webSVGMatrix
		switch name {
		case "matrix":
			if len(args) != 6 {
				continue
			}
			t = webSVGMatrix{a: args[0], b: args[1], c: args[2], d: args[3], e: args[4], f: args[5]}
		case "translate":
			ty := 0.0
			if len(args) > 1 {
				ty = args[1]
			}
			t = webSVGTranslate(args[0], ty)
		case "scale":
			sy := args[0]
			if len(args) > 1 {
				sy = args[1]
			}
			t = webSVGScale(args[0], sy)
		case "rotate":
			rad := args[0] * math.Pi / 180
			cos, sin := math.Cos(rad), math.Sin(rad)
			t = webSVGMatrix{a: cos, b: sin, c: -sin, d: cos}
			if len(args) >= 3 {
				t = webSVGTranslate(args[1], args[2]).mul(t).mul(webSVGTranslate(-args[1], -args[2]))
			}
		case "skewx":
			t = webSVGMatrix{a: 1, c: math.Tan(args[0] * math.Pi / 180), d: 1}
		case "skewy":
			t = webSVGMatrix{a: 1, b: math.Tan(args[0] * math.Pi / 180), d: 1}
		default:
			continue
		}
		m = m.mul(t)
	}
	return m
}

// webSVGViewBoxMatrix maps a viewBox onto a w×h viewport under
// preserveAspectRatio (default xMidYMid meet).
func webSVGViewBoxMatrix(vb [4]float64, w, h float64, par string) webSVGMatrix {
	if w <= 0 || h <= 0 || vb[2] <= 0 || vb[3] <= 0 {
		return webSVGIdentity
	}
	sx, sy := w/vb[2], h/vb[3]
	align, slice := "xmidymid", false
	for _, f := range strings.Fields(strings.ToLower(par)) {
		switch f {
		case "slice":
			slice = true
		case "meet", "defer":
		default:
			align = f
		}
	}
	if align == "none" {
		return webSVGScale(sx, sy).mul(webSVGTranslate(-vb[0], -vb[1]))
	}
	s := math.Min(sx, sy)
	if slice {
		s = math.Max(sx, sy)
	}
	ax, ay := 0.5, 0.5
	switch {
	case strings.Contains(align, "xmin"):
		ax = 0
	case strings.Contains(align, "xmax"):
		ax = 1
	}
	switch {
	case strings.Contains(align, "ymin"):
		ay = 0
	case strings.Contains(align, "ymax"):
		ay = 1
	}
	tx, ty := (w-vb[2]*s)*ax, (h-vb[3]*s)*ay
	return webSVGTranslate(tx, ty).mul(webSVGScale(s, s)).mul(webSVGTranslate(-vb[0], -vb[1]))
}

// webSVGScanner reads the packed number lists SVG allows ("M10-5.5.5e2,3").
type webSVGScanner struct {
	s string
	i int
}

func (sc *webSVGScanner) skipSep() {
	for sc.i < len(sc.s) {
		switch sc.s[sc.i] {
		case ' ', '\t', '\r', '\n', '\f', ',':
			sc.i++
		default:
			return
		}
	}
}

func (sc *webSVGScanner) number() (float64, bool) {
	sc.skipSep()
	start, i := sc.i, sc.i
	if i < len(sc.s) && (sc.s[i] == '+' || sc.s[i] == '-') {
		i++
	}
	digits := false
	for i < len(sc.s) && sc.s[i] >= '0' && sc.s[i] <= '9' {
		i++
		digits = true
	}
	if i < len(sc.s) && sc.s[i] == '.' {
		i++
		for i < len(sc.s) && sc.s[i] >= '0' && sc.s[i] <= '9' {
			i++
			digits = true
		}
	}
	if !digits {
		return 0, false
	}
	if i < len(sc.s) && (sc.s[i] == 'e' || sc.s[i] == 'E') {
		j := i + 1
		if j < len(sc.s) && (sc.s[j] == '+' || sc.s[j] == '-') {
			j++
		}
		if j < len(sc.s) && sc.s[j] >= '0' && sc.s[j] <= '9' {
			for j < len(sc.s) && sc.s[j] >= '0' && sc.s[j] <= '9' {
				j++
			}
			i = j
		}
	}
	f, err := strconv.ParseFloat(sc.s[start:i], 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	sc.i = i
	return f, true
}

// flag reads an arc flag, which may be packed against what follows ("a1 1 0 01 1 1").
func (sc *webSVGScanner) flag() (bool, bool) {
	sc.skipSep()
	if sc.i < len(sc.s) && (sc.s[sc.i] == '0' || sc.s[sc.i] == '1') {
		v := sc.s[sc.i] == '1'
		sc.i++
		return v, true
	}
	return false, false
}

func webSVGNumbers(s string) []float64 {
	sc := webSVGScanner{s: s}
	var out []float64
	for len(out) < 100000 {
		f, ok := sc.number()
		if !ok {
			break
		}
		out = append(out, f)
	}
	return out
}

// webSVGPt is a point in raster pixels.
type webSVGPt struct{ x, y float64 }

// webSVGPathSink flattens user-space geometry into raster-space polylines as
// it is built, and keeps the user-space bounding box gradients need.
type webSVGPathSink struct {
	m      webSVGMatrix
	budget *int
	polys  [][]webSVGPt
	closed []bool
	cur    []webSVGPt
	// The bounding box of the geometry in user units (control points
	// included, which is a little generous on curves).
	minX, minY, maxX, maxY float64
	hasBox                 bool
}

func (s *webSVGPathSink) note(x, y float64) {
	if !s.hasBox {
		s.minX, s.maxX, s.minY, s.maxY, s.hasBox = x, x, y, y, true
		return
	}
	s.minX, s.maxX = math.Min(s.minX, x), math.Max(s.maxX, x)
	s.minY, s.maxY = math.Min(s.minY, y), math.Max(s.maxY, y)
}

func (s *webSVGPathSink) flush(closed bool) {
	if len(s.cur) > 0 {
		s.polys = append(s.polys, s.cur)
		s.closed = append(s.closed, closed)
	}
	s.cur = nil
}

func (s *webSVGPathSink) device(px, py float64) {
	if *s.budget <= 0 || math.IsNaN(px) || math.IsNaN(py) || math.IsInf(px, 0) || math.IsInf(py, 0) {
		return
	}
	*s.budget--
	s.cur = append(s.cur, webSVGPt{px, py})
}

func (s *webSVGPathSink) moveTo(x, y float64) {
	s.flush(false)
	s.note(x, y)
	s.device(s.m.apply(x, y))
}

func (s *webSVGPathSink) lineTo(x, y float64) {
	s.note(x, y)
	s.device(s.m.apply(x, y))
}

func (s *webSVGPathSink) closePath() { s.flush(true) }

// curveSteps is how many chords a curve with a control polygon of length l
// raster pixels gets: about one every three pixels.
func webSVGCurveSteps(l float64) int {
	n := int(math.Ceil(l / 3))
	if n < 1 {
		n = 1
	}
	if n > 64 {
		n = 64
	}
	return n
}

func (s *webSVGPathSink) cubic(x0, y0, x1, y1, x2, y2, x3, y3 float64) {
	s.note(x1, y1)
	s.note(x2, y2)
	s.note(x3, y3)
	ax, ay := s.m.apply(x0, y0)
	bx, by := s.m.apply(x1, y1)
	cx, cy := s.m.apply(x2, y2)
	dx, dy := s.m.apply(x3, y3)
	n := webSVGCurveSteps(math.Hypot(bx-ax, by-ay) + math.Hypot(cx-bx, cy-by) + math.Hypot(dx-cx, dy-cy))
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		mt := 1 - t
		k0, k1, k2, k3 := mt*mt*mt, 3*mt*mt*t, 3*mt*t*t, t*t*t
		s.device(k0*ax+k1*bx+k2*cx+k3*dx, k0*ay+k1*by+k2*cy+k3*dy)
	}
}

func (s *webSVGPathSink) quad(x0, y0, x1, y1, x2, y2 float64) {
	s.note(x1, y1)
	s.note(x2, y2)
	ax, ay := s.m.apply(x0, y0)
	bx, by := s.m.apply(x1, y1)
	cx, cy := s.m.apply(x2, y2)
	n := webSVGCurveSteps(math.Hypot(bx-ax, by-ay) + math.Hypot(cx-bx, cy-by))
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		mt := 1 - t
		k0, k1, k2 := mt*mt, 2*mt*t, t*t
		s.device(k0*ax+k1*bx+k2*cx, k0*ay+k1*by+k2*cy)
	}
}

// arc draws an elliptical arc from (x1, y1) to (x2, y2) in the SVG endpoint
// parameterisation, converted to its centre form (SVG 1.1 F.6.5) and sampled
// by how large it is on screen.
func (s *webSVGPathSink) arc(x1, y1, rx, ry, phiDeg float64, large, sweep bool, x2, y2 float64) {
	if x1 == x2 && y1 == y2 {
		return
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	if rx == 0 || ry == 0 {
		s.lineTo(x2, y2)
		return
	}
	phi := phiDeg * math.Pi / 180
	cosP, sinP := math.Cos(phi), math.Sin(phi)
	dx2, dy2 := (x1-x2)/2, (y1-y2)/2
	x1p := cosP*dx2 + sinP*dy2
	y1p := -sinP*dx2 + cosP*dy2
	if lambda := (x1p*x1p)/(rx*rx) + (y1p*y1p)/(ry*ry); lambda > 1 {
		sq := math.Sqrt(lambda)
		rx, ry = rx*sq, ry*sq
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	coef := 0.0
	if den > 0 && num > 0 {
		coef = math.Sqrt(num / den)
	}
	if large == sweep {
		coef = -coef
	}
	cxp, cyp := coef*rx*y1p/ry, -coef*ry*x1p/rx
	cx := cosP*cxp - sinP*cyp + (x1+x2)/2
	cy := sinP*cxp + cosP*cyp + (y1+y2)/2
	angle := func(ux, uy, vx, vy float64) float64 { return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy) }
	theta := angle(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	delta := angle((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && delta > 0 {
		delta -= 2 * math.Pi
	} else if sweep && delta < 0 {
		delta += 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(delta) * math.Max(rx, ry) * s.m.scale() / 3))
	if n < 2 {
		n = 2
	}
	if n > 128 {
		n = 128
	}
	for i := 1; i <= n; i++ {
		if i == n {
			s.lineTo(x2, y2)
			break
		}
		t := theta + delta*float64(i)/float64(n)
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		s.lineTo(cosP*ex-sinP*ey+cx, sinP*ex+cosP*ey+cy)
	}
}

func (s *webSVGPathSink) ellipse(cx, cy, rx, ry float64) {
	if rx <= 0 || ry <= 0 {
		return
	}
	n := int(math.Ceil(2 * math.Pi * math.Max(rx, ry) * s.m.scale() / 3))
	if n < 12 {
		n = 12
	}
	if n > 160 {
		n = 160
	}
	s.moveTo(cx+rx, cy)
	for i := 1; i < n; i++ {
		t := 2 * math.Pi * float64(i) / float64(n)
		s.lineTo(cx+rx*math.Cos(t), cy+ry*math.Sin(t))
	}
	s.note(cx-rx, cy-ry)
	s.note(cx+rx, cy+ry)
	s.closePath()
}

func (s *webSVGPathSink) rect(x, y, w, h, rx, ry float64) {
	if rx <= 0 || ry <= 0 {
		s.moveTo(x, y)
		s.lineTo(x+w, y)
		s.lineTo(x+w, y+h)
		s.lineTo(x, y+h)
		s.closePath()
		return
	}
	s.moveTo(x+rx, y)
	s.lineTo(x+w-rx, y)
	s.arc(x+w-rx, y, rx, ry, 0, false, true, x+w, y+ry)
	s.lineTo(x+w, y+h-ry)
	s.arc(x+w, y+h-ry, rx, ry, 0, false, true, x+w-rx, y+h)
	s.lineTo(x+rx, y+h)
	s.arc(x+rx, y+h, rx, ry, 0, false, true, x, y+h-ry)
	s.lineTo(x, y+ry)
	s.arc(x, y+ry, rx, ry, 0, false, true, x+rx, y)
	s.closePath()
}

// webSVGPathData feeds path data to the sink. A malformed command ends the
// path where it went wrong, which is what browsers draw too.
func webSVGPathData(d string, s *webSVGPathSink) {
	sc := webSVGScanner{s: d}
	var cmd, prev byte
	var cx, cy, sx, sy, lcx, lcy float64
	needMove := false
	for steps := 0; steps < webSVGMaxEdges; steps++ {
		sc.skipSep()
		if sc.i >= len(d) {
			break
		}
		if c := d[sc.i]; (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			cmd = c
			sc.i++
		} else if cmd == 0 {
			return
		}
		up := cmd &^ 0x20
		rel := cmd >= 'a'
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		if up != 'M' && up != 'Z' && needMove {
			s.moveTo(cx, cy)
			needMove = false
		}
		switch up {
		case 'M':
			x, ok1 := sc.number()
			y, ok2 := sc.number()
			if !ok1 || !ok2 {
				return
			}
			x, y = x+ox, y+oy
			s.moveTo(x, y)
			cx, cy, sx, sy = x, y, x, y
			needMove = false
			// Further pairs after a moveto are lineto commands.
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'Z':
			s.closePath()
			cx, cy = sx, sy
			needMove = true
			cmd = 0
		case 'L':
			x, ok1 := sc.number()
			y, ok2 := sc.number()
			if !ok1 || !ok2 {
				return
			}
			cx, cy = x+ox, y+oy
			s.lineTo(cx, cy)
		case 'H':
			x, ok := sc.number()
			if !ok {
				return
			}
			if rel {
				x += cx
			}
			cx = x
			s.lineTo(cx, cy)
		case 'V':
			y, ok := sc.number()
			if !ok {
				return
			}
			if rel {
				y += cy
			}
			cy = y
			s.lineTo(cx, cy)
		case 'C', 'S':
			x1, y1 := 2*cx-lcx, 2*cy-lcy
			if prev != 'C' && prev != 'S' {
				x1, y1 = cx, cy
			}
			if up == 'C' {
				a, ok1 := sc.number()
				b, ok2 := sc.number()
				if !ok1 || !ok2 {
					return
				}
				x1, y1 = a+ox, b+oy
			}
			x2, ok1 := sc.number()
			y2, ok2 := sc.number()
			x, ok3 := sc.number()
			y, ok4 := sc.number()
			if !ok1 || !ok2 || !ok3 || !ok4 {
				return
			}
			x2, y2, x, y = x2+ox, y2+oy, x+ox, y+oy
			s.cubic(cx, cy, x1, y1, x2, y2, x, y)
			lcx, lcy = x2, y2
			cx, cy = x, y
		case 'Q', 'T':
			x1, y1 := 2*cx-lcx, 2*cy-lcy
			if prev != 'Q' && prev != 'T' {
				x1, y1 = cx, cy
			}
			if up == 'Q' {
				a, ok1 := sc.number()
				b, ok2 := sc.number()
				if !ok1 || !ok2 {
					return
				}
				x1, y1 = a+ox, b+oy
			}
			x, ok1 := sc.number()
			y, ok2 := sc.number()
			if !ok1 || !ok2 {
				return
			}
			x, y = x+ox, y+oy
			s.quad(cx, cy, x1, y1, x, y)
			lcx, lcy = x1, y1
			cx, cy = x, y
		case 'A':
			rx, ok1 := sc.number()
			ry, ok2 := sc.number()
			rot, ok3 := sc.number()
			large, ok4 := sc.flag()
			sweep, ok5 := sc.flag()
			x, ok6 := sc.number()
			y, ok7 := sc.number()
			if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !ok7 {
				return
			}
			x, y = x+ox, y+oy
			s.arc(cx, cy, rx, ry, rot, large, sweep, x, y)
			cx, cy = x, y
		default:
			return
		}
		if up != 'C' && up != 'S' && up != 'Q' && up != 'T' {
			lcx, lcy = cx, cy
		}
		prev = up
	}
}

// --- Gradients ---------------------------------------------------------------------

type webSVGStop struct {
	off float64
	c   [4]float64 // straight RGBA, 0..1
}

// webSVGGradient is a resolved linear or radial gradient. Coordinates are in
// bounding-box units unless userSpace is set.
type webSVGGradient struct {
	radial         bool
	x1, y1, x2, y2 float64
	cx, cy, r      float64
	userSpace      bool
	transform      webSVGMatrix
	stops          []webSVGStop
	spread         uint8 // 0 pad, 1 reflect, 2 repeat
}

func (g *webSVGGradient) at(t float64) [4]float64 {
	switch g.spread {
	case 1:
		t = math.Mod(math.Abs(t), 2)
		if t > 1 {
			t = 2 - t
		}
	case 2:
		t -= math.Floor(t)
	}
	stops := g.stops
	if t <= stops[0].off {
		return stops[0].c
	}
	for i := 1; i < len(stops); i++ {
		if t > stops[i].off {
			continue
		}
		a, b := stops[i-1], stops[i]
		span := b.off - a.off
		if span <= 1e-9 {
			return b.c
		}
		f := (t - a.off) / span
		return [4]float64{
			a.c[0] + (b.c[0]-a.c[0])*f, a.c[1] + (b.c[1]-a.c[1])*f,
			a.c[2] + (b.c[2]-a.c[2])*f, a.c[3] + (b.c[3]-a.c[3])*f,
		}
	}
	return stops[len(stops)-1].c
}

// --- Rasteriser ----------------------------------------------------------------------

// webSVGPaintFn is what a fill writes at one pixel: a solid colour, or a
// gradient read through the inverse of its raster transform.
type webSVGPaintFn struct {
	r, g, b, a float64
	grad       *webSVGGradient
	inv        webSVGMatrix
}

func (p *webSVGPaintFn) at(x, y int) (float64, float64, float64, float64) {
	if p.grad == nil {
		return p.r, p.g, p.b, p.a
	}
	gx, gy := p.inv.apply(float64(x)+0.5, float64(y)+0.5)
	g := p.grad
	t := 0.0
	if g.radial {
		if g.r > 0 {
			t = math.Hypot(gx-g.cx, gy-g.cy) / g.r
		} else {
			t = 1
		}
	} else {
		dx, dy := g.x2-g.x1, g.y2-g.y1
		if l2 := dx*dx + dy*dy; l2 > 0 {
			t = ((gx-g.x1)*dx + (gy-g.y1)*dy) / l2
		}
	}
	c := g.at(t)
	return c[0], c[1], c[2], c[3] * p.a
}

type webSVGEdge struct {
	x0, y0, y1, dxdy float64
	dir              int8
}

type webSVGCross struct {
	x   float64
	dir int8
}

// webSVGCanvas is the premultiplied RGBA picture and the scratch the filler
// reuses between shapes.
type webSVGCanvas struct {
	w, h   int
	img    *image.RGBA
	cov    []float32
	edges  []webSVGEdge
	active []int
	xs     []webSVGCross
}

// fill paints the union of polys (each implicitly closed) under the winding
// rule. Coverage is exact along each of webSVGSubScanlines sample lines per
// pixel row, which keeps edges smooth without a per-pixel area integral.
func (cv *webSVGCanvas) fill(polys [][]webSVGPt, evenOdd bool, paint *webSVGPaintFn) {
	cv.edges = cv.edges[:0]
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, poly := range polys {
		n := len(poly)
		if n < 2 {
			continue
		}
		for i := 0; i < n; i++ {
			a, b := poly[i], poly[(i+1)%n]
			if a.y == b.y {
				continue
			}
			dir := int8(1)
			if a.y > b.y {
				a, b, dir = b, a, -1
			}
			cv.edges = append(cv.edges, webSVGEdge{x0: a.x, y0: a.y, y1: b.y, dxdy: (b.x - a.x) / (b.y - a.y), dir: dir})
			minX, maxX = math.Min(minX, math.Min(a.x, b.x)), math.Max(maxX, math.Max(a.x, b.x))
			minY, maxY = math.Min(minY, a.y), math.Max(maxY, b.y)
		}
	}
	if len(cv.edges) == 0 {
		return
	}
	y0, y1 := int(math.Max(0, math.Floor(minY))), int(math.Min(float64(cv.h), math.Ceil(maxY)))
	x0, x1 := int(math.Max(0, math.Floor(minX))), int(math.Min(float64(cv.w), math.Ceil(maxX)+1))
	if y0 >= y1 || x0 >= x1 {
		return
	}
	sort.Slice(cv.edges, func(i, j int) bool { return cv.edges[i].y0 < cv.edges[j].y0 })
	cv.active = cv.active[:0]
	next := 0
	const sub = webSVGSubScanlines
	weight := float32(1.0 / float64(sub))
	for py := y0; py < y1; py++ {
		touched := false
		for s := 0; s < sub; s++ {
			sy := float64(py) + (float64(s)+0.5)/sub
			for next < len(cv.edges) && cv.edges[next].y0 <= sy {
				cv.active = append(cv.active, next)
				next++
			}
			k := 0
			for _, ei := range cv.active {
				if cv.edges[ei].y1 > sy {
					cv.active[k] = ei
					k++
				}
			}
			cv.active = cv.active[:k]
			if len(cv.active) < 2 {
				continue
			}
			cv.xs = cv.xs[:0]
			for _, ei := range cv.active {
				e := &cv.edges[ei]
				cv.xs = append(cv.xs, webSVGCross{x: e.x0 + (sy-e.y0)*e.dxdy, dir: e.dir})
			}
			if len(cv.xs) <= 16 {
				for i := 1; i < len(cv.xs); i++ {
					for j := i; j > 0 && cv.xs[j].x < cv.xs[j-1].x; j-- {
						cv.xs[j], cv.xs[j-1] = cv.xs[j-1], cv.xs[j]
					}
				}
			} else {
				sort.Slice(cv.xs, func(i, j int) bool { return cv.xs[i].x < cv.xs[j].x })
			}
			wind := 0
			for i := 0; i < len(cv.xs)-1; i++ {
				wind += int(cv.xs[i].dir)
				inside := wind != 0
				if evenOdd {
					inside = wind%2 != 0
				}
				if inside {
					cv.span(cv.xs[i].x, cv.xs[i+1].x, weight)
					touched = true
				}
			}
		}
		if touched {
			cv.composite(py, x0, x1, paint)
		}
	}
}

// span adds coverage weight over [xa, xb) on the current row, with the partial
// pixels at both ends weighted by how much of them the span covers.
func (cv *webSVGCanvas) span(xa, xb float64, weight float32) {
	if xa < 0 {
		xa = 0
	}
	if xb > float64(cv.w) {
		xb = float64(cv.w)
	}
	if xb <= xa {
		return
	}
	ia, ib := int(xa), int(xb)
	if ia == ib {
		cv.cov[ia] += float32(xb-xa) * weight
		return
	}
	cv.cov[ia] += float32(float64(ia+1)-xa) * weight
	for i := ia + 1; i < ib; i++ {
		cv.cov[i] += weight
	}
	if ib < cv.w {
		cv.cov[ib] += float32(xb-float64(ib)) * weight
	}
}

// composite blends the row's coverage into the picture (source over, the
// buffer is premultiplied) and clears it for the next row.
func (cv *webSVGCanvas) composite(py, x0, x1 int, paint *webSVGPaintFn) {
	row := cv.img.Pix[py*cv.img.Stride:]
	for x := x0; x < x1; x++ {
		c := cv.cov[x]
		if c == 0 {
			continue
		}
		cv.cov[x] = 0
		if c > 1 {
			c = 1
		}
		r, g, b, a := paint.at(x, py)
		a *= float64(c)
		if a <= 0 {
			continue
		}
		if a > 1 {
			a = 1
		}
		i := x * 4
		keep := 1 - a
		row[i] = webSVGByte(r*a*255 + float64(row[i])*keep)
		row[i+1] = webSVGByte(g*a*255 + float64(row[i+1])*keep)
		row[i+2] = webSVGByte(b*a*255 + float64(row[i+2])*keep)
		row[i+3] = webSVGByte(a*255 + float64(row[i+3])*keep)
	}
}

func webSVGByte(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// --- Stroker ---------------------------------------------------------------------------

func webSVGSignedArea(poly []webSVGPt) float64 {
	area := 0.0
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		area += a.x*b.y - b.x*a.y
	}
	return area / 2
}

func webSVGCircle(c webSVGPt, r float64) []webSVGPt {
	n := int(r*1.5) + 8
	if n > 48 {
		n = 48
	}
	out := make([]webSVGPt, n)
	for i := range out {
		t := 2 * math.Pi * float64(i) / float64(n)
		out[i] = webSVGPt{c.x + r*math.Cos(t), c.y + r*math.Sin(t)}
	}
	return out
}

// webSVGStroke turns polylines into the polygons their stroke covers: one quad
// per segment, the joins and the caps. The pieces overlap, and every piece is
// wound the same way, so a nonzero fill of all of them is their union.
func webSVGStroke(polys [][]webSVGPt, closed []bool, hw float64, lineCap, lineJoin uint8, miterLimit float64, budget *int) [][]webSVGPt {
	var out [][]webSVGPt
	add := func(poly []webSVGPt) {
		if len(poly) < 3 || *budget <= 0 {
			return
		}
		*budget -= len(poly)
		if webSVGSignedArea(poly) < 0 {
			for i, j := 0, len(poly)-1; i < j; i, j = i+1, j-1 {
				poly[i], poly[j] = poly[j], poly[i]
			}
		}
		out = append(out, poly)
	}
	unit := func(a, b webSVGPt) (float64, float64, bool) {
		dx, dy := b.x-a.x, b.y-a.y
		l := math.Hypot(dx, dy)
		if l < 1e-9 {
			return 0, 0, false
		}
		return dx / l, dy / l, true
	}
	for pi, raw := range polys {
		pts := make([]webSVGPt, 0, len(raw))
		for _, p := range raw {
			if n := len(pts); n > 0 && math.Abs(pts[n-1].x-p.x) < 1e-6 && math.Abs(pts[n-1].y-p.y) < 1e-6 {
				continue
			}
			pts = append(pts, p)
		}
		isClosed := pi < len(closed) && closed[pi]
		if n := len(pts); isClosed && n > 1 && math.Abs(pts[0].x-pts[n-1].x) < 1e-6 && math.Abs(pts[0].y-pts[n-1].y) < 1e-6 {
			pts = pts[:n-1]
		}
		n := len(pts)
		if n == 0 {
			continue
		}
		if n == 1 {
			// A zero-length subpath shows only its caps.
			switch lineCap {
			case 1:
				add(webSVGCircle(pts[0], hw))
			case 2:
				p := pts[0]
				add([]webSVGPt{{p.x - hw, p.y - hw}, {p.x + hw, p.y - hw}, {p.x + hw, p.y + hw}, {p.x - hw, p.y + hw}})
			}
			continue
		}
		segs := n - 1
		if isClosed {
			segs = n
		}
		for i := 0; i < segs; i++ {
			a, b := pts[i], pts[(i+1)%n]
			ux, uy, ok := unit(a, b)
			if !ok {
				continue
			}
			nx, ny := -uy*hw, ux*hw
			add([]webSVGPt{{a.x + nx, a.y + ny}, {b.x + nx, b.y + ny}, {b.x - nx, b.y - ny}, {a.x - nx, a.y - ny}})
		}
		join := func(prev, p, next webSVGPt) {
			u1x, u1y, ok1 := unit(prev, p)
			u2x, u2y, ok2 := unit(p, next)
			if !ok1 || !ok2 {
				return
			}
			cross := u1x*u2y - u1y*u2x
			if math.Abs(cross) < 1e-9 && u1x*u2x+u1y*u2y > 0 {
				return
			}
			if lineJoin == 1 {
				add(webSVGCircle(p, hw))
				return
			}
			// The outer side of the corner is away from the turn.
			side := 1.0
			if cross > 0 {
				side = -1
			}
			n1x, n1y := -u1y, u1x
			n2x, n2y := -u2y, u2x
			a := webSVGPt{p.x + side*n1x*hw, p.y + side*n1y*hw}
			b := webSVGPt{p.x + side*n2x*hw, p.y + side*n2y*hw}
			if lineJoin == 0 {
				mx, my := n1x+n2x, n1y+n2y
				if l := math.Hypot(mx, my); l > 1e-9 && 2/l <= miterLimit {
					k := side * 2 * hw / (l * l)
					add([]webSVGPt{p, a, {p.x + mx*k, p.y + my*k}, b})
					return
				}
			}
			add([]webSVGPt{p, a, b})
		}
		if isClosed {
			for i := 0; i < n; i++ {
				join(pts[(i-1+n)%n], pts[i], pts[(i+1)%n])
			}
			continue
		}
		for i := 1; i < n-1; i++ {
			join(pts[i-1], pts[i], pts[i+1])
		}
		capAt := func(p, inner webSVGPt) {
			ux, uy, ok := unit(inner, p)
			if !ok {
				return
			}
			switch lineCap {
			case 1:
				add(webSVGCircle(p, hw))
			case 2:
				nx, ny := -uy*hw, ux*hw
				ex, ey := p.x+ux*hw, p.y+uy*hw
				add([]webSVGPt{{p.x + nx, p.y + ny}, {ex + nx, ey + ny}, {ex - nx, ey - ny}, {p.x - nx, p.y - ny}})
			}
		}
		capAt(pts[0], pts[1])
		capAt(pts[n-1], pts[n-2])
	}
	return out
}

// --- Renderer ---------------------------------------------------------------------------

type webSVGRenderer struct {
	canvas   *webSVGCanvas
	doc      *webSVGDoc
	defs     []*webSVGDoc
	budget   int
	useDepth int
}

// lookup finds an id in the element's own document, then the main document,
// then the page's sprite sheets.
func (r *webSVGRenderer) lookup(doc *webSVGDoc, id string) *webSVGNode {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if doc != nil {
		if n := doc.ids[id]; n != nil {
			return n
		}
	}
	if n := r.doc.ids[id]; n != nil {
		return n
	}
	for _, d := range r.defs {
		if n := d.ids[id]; n != nil {
			return n
		}
	}
	return nil
}

func (r *webSVGRenderer) render(n *webSVGNode, st webSVGState, m webSVGMatrix, depth int) {
	if n == nil || depth > webSVGMaxDepth || r.budget <= 0 {
		return
	}
	switch n.name {
	case "g", "a", "switch", "svg", "use", "path", "rect", "circle", "ellipse", "line", "polyline", "polygon":
	default:
		// defs, symbol, gradients, text, image, filters…: not drawn here.
		return
	}
	props := webSVGDeclared(n)
	if strings.EqualFold(strings.TrimSpace(props["display"]), "none") {
		return
	}
	st = webSVGApply(st, props)
	if t := n.attrs["transform"]; t != "" && n.name != "svg" {
		m = m.mul(webSVGParseTransform(t))
	}
	switch n.name {
	case "g", "a":
		for _, c := range n.children {
			r.render(c, st, m, depth+1)
		}
	case "switch":
		// The first child is the one a reader without feature tests picks.
		for _, c := range n.children {
			if c.name != "desc" && c.name != "title" && c.name != "metadata" {
				r.render(c, st, m, depth+1)
				break
			}
		}
	case "svg":
		x, _ := webSVGLength(n.attrs["x"], st.vw)
		y, _ := webSVGLength(n.attrs["y"], st.vh)
		w, okW := webSVGLength(n.attrs["width"], st.vw)
		h, okH := webSVGLength(n.attrs["height"], st.vh)
		if !okW {
			w = st.vw
		}
		if !okH {
			h = st.vh
		}
		r.viewport(n, st, m.mul(webSVGTranslate(x, y)), w, h, depth)
	case "use":
		r.use(n, st, m, depth)
	default:
		r.shape(n, st, m)
	}
}

// viewport draws the children of a nested <svg> or an instantiated <symbol>
// in a w×h viewport whose origin m already includes.
func (r *webSVGRenderer) viewport(n *webSVGNode, st webSVGState, m webSVGMatrix, w, h float64, depth int) {
	if w <= 0 || h <= 0 {
		return
	}
	if vb, ok := webSVGViewBox(n.attrs["viewbox"]); ok {
		m = m.mul(webSVGViewBoxMatrix(vb, w, h, n.attrs["preserveaspectratio"]))
		st.vw, st.vh = vb[2], vb[3]
	} else {
		st.vw, st.vh = w, h
	}
	for _, c := range n.children {
		r.render(c, st, m, depth+1)
	}
}

// use draws the element a <use> references: a symbol (or svg) as a new
// viewport sized by the <use>, anything else as if it stood here.
func (r *webSVGRenderer) use(n *webSVGNode, st webSVGState, m webSVGMatrix, depth int) {
	if r.useDepth >= webSVGMaxUseDepth {
		return
	}
	href := n.attrs["href"]
	hash := strings.IndexByte(href, '#')
	if hash < 0 {
		return
	}
	target := r.lookup(n.doc, href[hash+1:])
	if target == nil || target == n {
		return
	}
	x, _ := webSVGLength(n.attrs["x"], st.vw)
	y, _ := webSVGLength(n.attrs["y"], st.vh)
	m = m.mul(webSVGTranslate(x, y))
	r.useDepth++
	defer func() { r.useDepth-- }()
	if target.name != "symbol" && target.name != "svg" {
		r.render(target, st, m, depth+1)
		return
	}
	props := webSVGDeclared(target)
	if strings.EqualFold(strings.TrimSpace(props["display"]), "none") && target.name != "symbol" {
		return
	}
	st = webSVGApply(st, props)
	w, okW := webSVGLength(n.attrs["width"], st.vw)
	h, okH := webSVGLength(n.attrs["height"], st.vh)
	if !okW {
		if tw, ok := webSVGLength(target.attrs["width"], st.vw); ok {
			w = tw
		} else {
			w = st.vw
		}
	}
	if !okH {
		if th, ok := webSVGLength(target.attrs["height"], st.vh); ok {
			h = th
		} else {
			h = st.vh
		}
	}
	r.viewport(target, st, m, w, h, depth)
}

// shape builds one basic shape or path and paints its fill, then its stroke.
func (r *webSVGRenderer) shape(n *webSVGNode, st webSVGState, m webSVGMatrix) {
	sink := &webSVGPathSink{m: m, budget: &r.budget}
	length := func(name string, ref float64) float64 {
		v, _ := webSVGLength(n.attrs[name], ref)
		return v
	}
	switch n.name {
	case "path":
		webSVGPathData(n.attrs["d"], sink)
	case "rect":
		x, y := length("x", st.vw), length("y", st.vh)
		w, h := length("width", st.vw), length("height", st.vh)
		if w <= 0 || h <= 0 {
			return
		}
		rx, okX := webSVGLength(n.attrs["rx"], st.vw)
		ry, okY := webSVGLength(n.attrs["ry"], st.vh)
		switch {
		case okX && !okY:
			ry = rx
		case okY && !okX:
			rx = ry
		}
		rx, ry = math.Max(0, math.Min(rx, w/2)), math.Max(0, math.Min(ry, h/2))
		sink.rect(x, y, w, h, rx, ry)
	case "circle":
		radius := length("r", st.diag())
		sink.ellipse(length("cx", st.vw), length("cy", st.vh), radius, radius)
	case "ellipse":
		sink.ellipse(length("cx", st.vw), length("cy", st.vh), length("rx", st.vw), length("ry", st.vh))
	case "line":
		sink.moveTo(length("x1", st.vw), length("y1", st.vh))
		sink.lineTo(length("x2", st.vw), length("y2", st.vh))
	case "polyline", "polygon":
		nums := webSVGNumbers(n.attrs["points"])
		for i := 0; i+1 < len(nums); i += 2 {
			if i == 0 {
				sink.moveTo(nums[i], nums[i+1])
			} else {
				sink.lineTo(nums[i], nums[i+1])
			}
		}
		if n.name == "polygon" {
			sink.closePath()
		}
	}
	sink.flush(false)
	if len(sink.polys) == 0 || !st.visible {
		return
	}
	if st.fill.kind != webSVGPaintNone && n.name != "line" {
		if paint, ok := r.paint(n, st.fill, st, st.fillOpacity*st.opacity, sink, m); ok {
			r.canvas.fill(sink.polys, st.evenOdd, &paint)
		}
	}
	if st.stroke.kind != webSVGPaintNone && st.strokeWidth > 0 {
		hw := st.strokeWidth * m.scale() / 2
		if hw < 0.35 {
			// A hairline still shows: a 1-unit stroke on a 16px icon drawn
			// from a 512-unit viewBox would otherwise vanish.
			hw = 0.35
		}
		polys := webSVGStroke(sink.polys, sink.closed, hw, st.lineCap, st.lineJoin, st.miterLimit, &r.budget)
		if len(polys) > 0 {
			if paint, ok := r.paint(n, st.stroke, st, st.strokeOpacity*st.opacity, sink, m); ok {
				r.canvas.fill(polys, false, &paint)
			}
		}
	}
}

// paint resolves a fill or stroke for one shape. ok is false when nothing
// should be painted.
func (r *webSVGRenderer) paint(n *webSVGNode, p webSVGPaint, st webSVGState, alpha float64, sink *webSVGPathSink, m webSVGMatrix) (webSVGPaintFn, bool) {
	if alpha <= 0 {
		return webSVGPaintFn{}, false
	}
	solid := func(c sdl.Color) (webSVGPaintFn, bool) {
		a := float64(c.A) / 255 * alpha
		if a <= 0 {
			return webSVGPaintFn{}, false
		}
		return webSVGPaintFn{r: float64(c.R) / 255, g: float64(c.G) / 255, b: float64(c.B) / 255, a: a}, true
	}
	switch p.kind {
	case webSVGPaintColor:
		return solid(p.color)
	case webSVGPaintCurrent:
		return solid(st.color)
	case webSVGPaintURL:
		g := r.gradient(n.doc, p.ref, st)
		if g == nil || len(g.stops) == 0 {
			switch p.fbKind {
			case webSVGPaintColor:
				return solid(p.fbColor)
			case webSVGPaintCurrent:
				return solid(st.color)
			}
			return webSVGPaintFn{}, false
		}
		if len(g.stops) == 1 {
			c := g.stops[0].c
			return webSVGPaintFn{r: c[0], g: c[1], b: c[2], a: c[3] * alpha}, c[3]*alpha > 0
		}
		full := m
		if !g.userSpace {
			bw, bh := sink.maxX-sink.minX, sink.maxY-sink.minY
			if !sink.hasBox || bw <= 0 || bh <= 0 {
				// A bounding box with no area: the average of the ramp.
				c := g.at(0.5)
				return webSVGPaintFn{r: c[0], g: c[1], b: c[2], a: c[3] * alpha}, c[3]*alpha > 0
			}
			full = full.mul(webSVGTranslate(sink.minX, sink.minY)).mul(webSVGScale(bw, bh))
		}
		full = full.mul(g.transform)
		inv, ok := full.invert()
		if !ok {
			return webSVGPaintFn{}, false
		}
		return webSVGPaintFn{a: alpha, grad: g, inv: inv}, true
	}
	return webSVGPaintFn{}, false
}

// gradient resolves a gradient and its href chain: attributes from the
// nearest gradient that sets them, stops from the first one that has any.
func (r *webSVGRenderer) gradient(doc *webSVGDoc, id string, st webSVGState) *webSVGGradient {
	isGradient := func(n *webSVGNode) bool {
		return n != nil && (n.name == "lineargradient" || n.name == "radialgradient")
	}
	n := r.lookup(doc, id)
	if !isGradient(n) {
		return nil
	}
	chain := []*webSVGNode{n}
	for cur := n; len(chain) < 6; {
		href := cur.attrs["href"]
		hash := strings.IndexByte(href, '#')
		if hash < 0 {
			break
		}
		next := r.lookup(cur.doc, href[hash+1:])
		if !isGradient(next) || next == cur {
			break
		}
		chain = append(chain, next)
		cur = next
	}
	attr := func(name string) (string, bool) {
		for _, c := range chain {
			if v, ok := c.attrs[name]; ok {
				return v, true
			}
		}
		return "", false
	}
	g := &webSVGGradient{radial: n.name == "radialgradient", transform: webSVGIdentity}
	if v, ok := attr("gradientunits"); ok {
		g.userSpace = strings.EqualFold(strings.TrimSpace(v), "userSpaceOnUse")
	}
	if v, ok := attr("gradienttransform"); ok {
		g.transform = webSVGParseTransform(v)
	}
	if v, ok := attr("spreadmethod"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "reflect":
			g.spread = 1
		case "repeat":
			g.spread = 2
		}
	}
	coord := func(name, def string, ref float64) float64 {
		v, ok := attr(name)
		if !ok {
			v = def
		}
		if g.userSpace {
			if f, valid := webSVGLength(v, ref); valid {
				return f
			}
			f, _ := webSVGLength(def, ref)
			return f
		}
		if f, valid := webSVGFraction(v); valid {
			return f
		}
		f, _ := webSVGFraction(def)
		return f
	}
	if g.radial {
		g.cx, g.cy, g.r = coord("cx", "50%", st.vw), coord("cy", "50%", st.vh), coord("r", "50%", st.diag())
	} else {
		g.x1, g.y1 = coord("x1", "0%", st.vw), coord("y1", "0%", st.vh)
		g.x2, g.y2 = coord("x2", "100%", st.vw), coord("y2", "0%", st.vh)
	}
	for _, c := range chain {
		last := 0.0
		for _, child := range c.children {
			if child.name != "stop" || len(g.stops) >= 64 {
				continue
			}
			props := webSVGDeclared(child)
			off := 0.0
			if v, ok := child.attrs["offset"]; ok {
				off, _ = webSVGFraction(v)
			}
			off = math.Max(last, math.Max(0, math.Min(1, off)))
			last = off
			col := sdl.Color{A: 255}
			if v, ok := props["stop-color"]; ok {
				switch strings.ToLower(strings.TrimSpace(v)) {
				case "currentcolor":
					col = st.color
				case "transparent":
					col = sdl.Color{}
				default:
					if pc, valid := parseWebColor(v); valid {
						col = pc
					}
				}
			}
			opacity := 1.0
			if v, ok := props["stop-opacity"]; ok {
				opacity = webSVGOpacity(v, 1)
			}
			g.stops = append(g.stops, webSVGStop{off: off, c: [4]float64{
				float64(col.R) / 255, float64(col.G) / 255, float64(col.B) / 255, float64(col.A) / 255 * opacity,
			}})
		}
		if len(g.stops) > 0 {
			break
		}
	}
	return g
}

// webSVGIntrinsic is a document's intrinsic size in CSS px: its width and
// height, completed from the viewBox's ratio, or the viewBox itself, or the
// 300×150 every replaced element without a size gets.
func webSVGIntrinsic(root *webSVGNode) (w, h float64, vb [4]float64, hasVB bool) {
	vb, hasVB = webSVGViewBox(root.attrs["viewbox"])
	aw, okW := webSVGAttrLength(root.attrs["width"], 16)
	ah, okH := webSVGAttrLength(root.attrs["height"], 16)
	if !okW || aw <= 0 {
		aw = 0
	}
	if !okH || ah <= 0 {
		ah = 0
	}
	switch {
	case aw > 0 && ah > 0:
		w, h = aw, ah
	case aw > 0 && hasVB:
		w, h = aw, aw*vb[3]/vb[2]
	case ah > 0 && hasVB:
		w, h = ah*vb[2]/vb[3], ah
	case aw > 0:
		w, h = aw, 150
	case ah > 0:
		w, h = 300, ah
	case hasVB:
		w, h = vb[2], vb[3]
	default:
		w, h = 300, 150
	}
	w, h = math.Max(1, math.Min(w, 8192)), math.Max(1, math.Min(h, 8192))
	return w, h, vb, hasVB
}

// webRasterizeSVG draws an SVG document into a PNG. meta is the document's
// intrinsic size in CSS px; the PNG is drawn at webSVGRasterScale times the
// size the page shows it at (opt.W/opt.H) or its intrinsic size, capped.
func webRasterizeSVG(data []byte, opt webSVGOptions) (out []byte, meta webImageMeta, ok bool) {
	defer func() {
		if recover() != nil {
			out, meta, ok = nil, webImageMeta{}, false
		}
	}()
	if len(data) == 0 || len(data) > webSVGMaxMarkup {
		return nil, webImageMeta{}, false
	}
	doc := webSVGParseDoc(data)
	if doc == nil || doc.root == nil {
		return nil, webImageMeta{}, false
	}
	root := doc.root
	iw, ih, vb, hasVB := webSVGIntrinsic(root)
	meta = webImageMeta{HasSize: true, W: int(math.Max(1, math.Round(iw))), H: int(math.Max(1, math.Round(ih)))}
	vw, vh := iw, ih
	switch {
	case opt.W > 0 && opt.H > 0:
		vw, vh = opt.W, opt.H
	case opt.W > 0:
		vw, vh = opt.W, opt.W*ih/iw
	case opt.H > 0:
		vw, vh = opt.H*iw/ih, opt.H
	}
	scale := webSVGRasterScale
	if long := math.Max(vw, vh) * scale; long > webSVGMaxRaster {
		scale = webSVGMaxRaster / math.Max(vw, vh)
	}
	pw := int(math.Max(1, math.Min(webSVGMaxRaster, math.Ceil(vw*scale))))
	ph := int(math.Max(1, math.Min(webSVGMaxRaster, math.Ceil(vh*scale))))
	canvas := &webSVGCanvas{w: pw, h: ph, img: image.NewRGBA(image.Rect(0, 0, pw, ph)), cov: make([]float32, pw+1)}
	r := &webSVGRenderer{canvas: canvas, doc: doc, defs: opt.Defs, budget: webSVGMaxEdges}
	st := webSVGState{
		fill:          webSVGPaint{kind: webSVGPaintColor, color: sdl.Color{A: 255}},
		fillOpacity:   1,
		strokeOpacity: 1,
		opacity:       1,
		strokeWidth:   1,
		miterLimit:    4,
		color:         sdl.Color{A: 255},
		visible:       true,
	}
	if opt.HasColor {
		st.color = opt.Color
	}
	// The root's own declarations, then the page's CSS fill and stroke, which
	// beat presentation attributes as any author style does.
	props := webSVGDeclared(root)
	if opt.Fill != "" {
		props["fill"] = opt.Fill
	}
	if opt.Stroke != "" {
		props["stroke"] = opt.Stroke
	}
	if !strings.EqualFold(strings.TrimSpace(props["display"]), "none") {
		st = webSVGApply(st, props)
		m := webSVGScale(scale, scale)
		if hasVB {
			m = m.mul(webSVGViewBoxMatrix(vb, vw, vh, root.attrs["preserveaspectratio"]))
			st.vw, st.vh = vb[2], vb[3]
		} else {
			st.vw, st.vh = vw, vh
		}
		for _, c := range root.children {
			r.render(c, st, m, 1)
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, canvas.img); err != nil {
		return nil, webImageMeta{}, false
	}
	return buf.Bytes(), meta, true
}
