package main

// webpage_adremove.go — ads are cut out of a page before it is drawn.
//
// adblock.go and brw_adblock.go keep ad and tracker requests off the wire, but
// the room a page keeps for its ads was still in its markup: a slot reserving
// 250px under an "Advertisement" line, a sponsored card in a grid, a picture
// whose ad server was blocked and so drew as an empty placeholder, a Taboola
// feed under the article. This pass cuts those elements, with everything inside
// them, out of the markup before the reader parses it. Nothing of an ad is laid
// out, painted, linked or fetched, and the page closes up round the gap as if
// the ad had never been there: a grid's other cards move up, an article's
// paragraphs meet, text no longer wraps round an empty floated slot.
//
// Removing, not hiding, is the point. A display:none ad still counts for
// :nth-child, so a card grid styled `:nth-child(3n) { margin-right: 0 }` goes
// out of step behind it; a cut one never existed. Where a cut ad was an item of
// a grid a marker comment stays behind (webAdCutMarker), and layout closes the
// column tracks nothing is left in (layoutGridItems): an article whose ad rail
// is gone takes the rail's width instead of leaving a blank column.
//
// What counts as an ad, from most to least certain:
//
//   - ad-tech markers: AdSense's <ins class="adsbygoogle">, Google Publisher
//     Tag slots (div-gpt-ad…, google_ads_iframe…), Taboola, Outbrain, Ezoic,
//     AdThrive, AMP's <amp-ad>, the search engines' ad blocks, pictures,
//     videos and frames whose server the request filter blocks, tracking
//     pixels, links into ad click servers. Cut unless they hold a page's
//     worth of text or a main heading.
//   - names that say advertisement, sponsored or advertorial, and custom
//     elements named for ads (<shreddit-ad-post>): cut unless they hold more
//     than a card's text, a main heading or <main>.
//   - names built round "ad"/"ads" (ad-slot, top-ad, AdContainer, data-ad-*):
//     cut only when nothing but ad furniture is inside — no text but labels,
//     no picture, video, frame or control. A classifieds listing, whose
//     "ad-card" holds a title and a price, stays; so does anything whose name
//     also names a listing's parts or a page state (ad-title, has-ads,
//     ad-free).
//   - a block holding nothing but a label ("Advertisement", "Sponsored",
//     "Story continues below advertisement", "Anzeige", "広告"…), and a wrapper
//     holding nothing but ads and their labels: the frame round the ad goes
//     with it. A label word inside a sentence stays.
//
// Every verdict is made at the element's end, when its content is known, over
// exactly the markup that would be cut: a page whose tags do not nest cleanly
// can only make an element look bigger, so it is kept rather than cut. The walk
// is webParser.walk's (same tokenizer, raw elements and end-tag matching), so
// the elements are the parser's own.
//
// It runs once per load, after the page's scripts (webdom_runtime.go) and
// before the text, link, style sheet and layout passes (textbrowser.go), under
// the switch and per-site exception of the request filter (browserAdBlockOff);
// every element cut counts toward the page's "blocked" tally.

import (
	htmlstd "html"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// webAdCutMarker stands where an element was cut out. webParser.walk notes it,
// so a grid that lost an item to an ad closes the tracks nothing is left in.
const webAdCutMarker = "<!--jh:ad-->"

// What an element's tag, names, attributes or address say about it, from
// least to most certain.
const (
	webAdNone uint8 = iota
	// webAdGeneric is a name built round "ad" (ad-slot, topAd, data-ad-name):
	// cut only when nothing but ad furniture is inside.
	webAdGeneric
	// webAdExplicit is a name that says advertisement, sponsored or
	// advertorial: cut unless it holds more than a card's worth of text.
	webAdExplicit
	// webAdStrong is an ad-tech marker or an ad server's address.
	webAdStrong
)

// webAdStrongLetters and webAdExplicitLetters are the most text, counted in
// letters and digits, a marked ad may hold and still be cut: a recommendation
// feed's headlines fit under the first, a sponsored card under the second, an
// article under neither.
const (
	webAdStrongLetters   = 8000
	webAdExplicitLetters = 1200
	webAdExplicitElems   = 400
)

// webAdWrapDepth and webAdWrapElems bound a wrapper cut for holding nothing
// but ads: at most this many levels above the nearest one, with at most this
// many elements inside.
const (
	webAdWrapDepth = 6
	webAdWrapElems = 80
)

// webAdMaxDepth gives up on a page nested deeper than any real one.
const webAdMaxDepth = 4096

// webAdLabelLetters and webAdLabelBytes bound the text run read as a possible
// label; anything longer is content.
const (
	webAdLabelLetters = 64
	webAdLabelBytes   = 256
)

// The kinds of text run (webAdTextKind).
const (
	// webAdTextBlank is whitespace, punctuation, a "×" or a "Close": neither
	// content nor a label.
	webAdTextBlank = iota
	// webAdTextShort is a bare "Ad" or "Ads".
	webAdTextShort
	// webAdTextLabel is an ad label.
	webAdTextLabel
	// webAdTextContent is anything else.
	webAdTextContent
)

// The sorts of word group in a name (webAdPart).
const (
	webAdPartForeign = iota
	webAdPartAd
	webAdPartCompanion
	webAdPartExcluded
)

// webAdSpan is a stretch of markup to cut: an element, or a label's text run.
type webAdSpan struct {
	start, end int
	element    bool
}

// webAdFrame is one open element of the walk, with what its content so far
// says about it.
type webAdFrame struct {
	tag   string
	start int
	// signal is what the element's own tag, names and address say.
	signal uint8
	// block: a block-level element, whose label text stands on a line of its
	// own. keep: never cut itself (body, main, a table cell); hold: nor any
	// element round it (the page's body, its <main>).
	block, keep, hold bool
	// content counts the letters and digits of text that is neither a label
	// nor inside a cut element; inline is the part of it that is running text
	// — the element's own text and its inline children's.
	content, inline int
	// label: some text is an ad label; long: one of them is more than a bare
	// "Ad"; own: the element carries one itself — in its own text, as an
	// inline child or as a label line cut from it — rather than somewhere
	// down in a child that stays (a listing's "Promoted" badge in a grid).
	label, long, own bool
	// img: a picture that is not an ad; media: a video, frame, object or form
	// control that is not one; heavy: an <h1> or <main> inside.
	img, media, heavy bool
	// ads counts the elements cut inside, near is how many levels down the
	// nearest is (0 for none) and elems how many elements are inside.
	ads, near, elems int
	// pending are label lines — an inline element or a text run holding only
	// a label — whose fate is this element's: they go unless it has running
	// text for them to be part of.
	pending []webAdSpan
}

// webAdRemover is one page's pass.
type webAdRemover struct {
	src    string
	page   string
	base   *url.URL
	stack  []webAdFrame
	cuts   []webAdSpan
	inHead bool
}

// webRemoveAds returns the page's markup with its ads cut out, and how many
// elements went. pageURL is the page's address: it resolves the addresses of
// pictures, frames and links and tells which hosts are the page's own. The
// caller decides whether the page is filtered at all (browserAdBlockOff).
func webRemoveAds(source, pageURL string) (string, int) {
	if source == "" {
		return source, 0
	}
	r := &webAdRemover{src: source, page: pageURL}
	if base, err := url.Parse(pageURL); err == nil {
		r.base = base
	}
	if !r.walk() {
		return source, 0
	}
	return r.output()
}

// walk reads the markup as webParser.walk does and decides every element at
// its end. It reports false for a page nested too deeply to judge.
func (r *webAdRemover) walk() bool {
	src := r.src
	r.stack = append(r.stack, webAdFrame{tag: "#document", block: true, keep: true, hold: true})
	for i := 0; i < len(src); {
		if src[i] != '<' {
			end := strings.IndexByte(src[i:], '<')
			if end < 0 {
				end = len(src)
			} else {
				end += i
			}
			r.text(i, end)
			i = end
			continue
		}
		if strings.HasPrefix(src[i:], "<!--") {
			if end := strings.Index(src[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
			} else {
				i = len(src)
			}
			continue
		}
		if strings.HasPrefix(src[i:], "<!") || strings.HasPrefix(src[i:], "<?") {
			if end := strings.IndexByte(src[i:], '>'); end >= 0 {
				i += end + 1
			} else {
				i = len(src)
			}
			continue
		}
		end := findTextBrowserTagEnd(src, i+1)
		if end < 0 {
			break
		}
		start := i
		name, closing, selfClose, attrs := parseWebTag(src[i+1 : end])
		i = end + 1
		if name == "" {
			continue
		}
		if closing {
			r.endTag(name, start, i)
			continue
		}
		// Raw elements end where walk ends them: a whole <svg>, a frame's or a
		// video's body, a script.
		if raw, next := r.rawEnd(name, selfClose, i); raw {
			f := r.open(name, attrs, start, src[i:next])
			r.settle(&f, next, true)
			i = next
			continue
		}
		if name == "head" {
			r.inHead = true
		} else {
			// An unclosed <head> ends at the first element that cannot live
			// in it, as in webParser.startTag.
			if r.inHead && !webHeadContentElement(name) {
				r.endTag("head", start, start)
				r.inHead = false
			}
			if name == "body" || name == "html" {
				r.inHead = false
			}
		}
		f := r.open(name, attrs, start, "")
		if name != "head" && (selfClose || webVoidElement(name)) {
			r.settle(&f, i, true)
			continue
		}
		if len(r.stack) >= webAdMaxDepth {
			return false
		}
		r.stack = append(r.stack, f)
	}
	// Whatever is still open ends with the page.
	for len(r.stack) > 1 {
		r.close(len(src), false)
	}
	return true
}

// rawEnd reports whether walk reads the element starting at i as a raw one
// (an <svg>, a placeholder, a script-like element) and where it ends.
func (r *webAdRemover) rawEnd(name string, selfClose bool, i int) (bool, int) {
	switch {
	case name == "svg" || webPlaceholderElement(name):
		if selfClose || webVoidElement(name) {
			return true, i
		}
	case name == "title" || name == "style" || webRawTextElement(name):
	default:
		return false, i
	}
	_, next, _ := webRawElement(r.src, i, name)
	return true, next
}

// endTag closes the innermost open element with this name, and every element
// left open inside it, as webParser.endTag does; one with no open element is
// ignored. start is where the end tag begins, after where it ends.
func (r *webAdRemover) endTag(name string, start, after int) {
	k := -1
	for i := len(r.stack) - 1; i > 0; i-- {
		if r.stack[i].tag == name {
			k = i
			break
		}
	}
	if k < 0 {
		return
	}
	for len(r.stack)-1 > k {
		r.close(start, false)
	}
	r.close(after, true)
}

// close ends the innermost open element at end — just past its end tag when
// explicit, else where the tag that closed it begins — and settles it.
func (r *webAdRemover) close(end int, explicit bool) {
	n := len(r.stack) - 1
	f := r.stack[n]
	r.stack = r.stack[:n]
	if f.tag == "head" {
		r.inHead = false
	}
	r.settle(&f, end, explicit)
}

// text adds a text run to the innermost open element.
func (r *webAdRemover) text(start, end int) {
	f := &r.stack[len(r.stack)-1]
	letters, kind := webAdTextKind(r.src[start:end])
	switch kind {
	case webAdTextContent:
		f.content += letters
		f.inline += letters
	case webAdTextLabel:
		f.label, f.long, f.own = true, true, true
		// A label run straight inside a block is a line of its own unless
		// the block has running text; inside an inline element it is that
		// element's to decide.
		if f.block {
			f.pending = append(f.pending, webAdSpan{start: start, end: end})
		}
	case webAdTextShort:
		f.label, f.own = true, true
	}
}

// settle decides an element at its end and hands what it holds to the element
// round it.
func (r *webAdRemover) settle(f *webAdFrame, end int, explicit bool) {
	cut, label := webAdVerdict(f, explicit)
	if !cut && !label && f.inline == 0 {
		// The element stays, and has no running text: its label lines go.
		r.cuts = append(r.cuts, f.pending...)
	}
	parent := &r.stack[len(r.stack)-1]
	switch {
	case cut:
		r.cuts = append(r.cuts, webAdSpan{start: f.start, end: end, element: true})
		parent.ads++
		parent.near = 1
		parent.elems++
		// A label line cut from a box still says what the box was.
		if f.label && f.content == 0 && f.ads == 0 {
			parent.own = true
		}
	case label:
		parent.pending = append(parent.pending, webAdSpan{start: f.start, end: end})
		parent.label, parent.own = true, true
		parent.long = parent.long || f.long
		parent.elems++
	default:
		parent.content += f.content
		if !f.block {
			parent.inline += f.content
		}
		parent.label = parent.label || f.label
		parent.long = parent.long || f.long
		parent.img = parent.img || f.img
		parent.media = parent.media || f.media
		parent.heavy = parent.heavy || f.heavy || f.tag == "h1" || f.tag == "main"
		parent.hold = parent.hold || f.hold
		if f.ads > 0 {
			parent.ads += f.ads
			if d := f.near + 1; parent.near == 0 || d < parent.near {
				parent.near = d
			}
		}
		parent.elems += f.elems + 1
	}
}

// webAdVerdict decides an element at its end: cut it — an ad, a wrapper of
// ads, a block holding only a label — or leave an inline label for the
// element round it to decide. explicit is false for an element its own end
// tag never closed: it is cut only when it holds nothing but ad furniture.
func webAdVerdict(f *webAdFrame, explicit bool) (cut, label bool) {
	if f.keep || f.hold {
		return false, false
	}
	switch f.signal {
	case webAdStrong:
		if explicit && f.content <= webAdStrongLetters && !f.heavy {
			return true, false
		}
	case webAdExplicit:
		if explicit && f.content <= webAdExplicitLetters && !f.heavy && f.elems <= webAdExplicitElems {
			return true, false
		}
	case webAdGeneric:
		// A box named for an ad that carries an ad label of its own is a
		// native ad: the sponsored card goes whole, not just its "Sponsored"
		// line.
		if explicit && f.own && f.content <= webAdExplicitLetters && !f.heavy && f.elems <= webAdExplicitElems {
			return true, false
		}
	}
	// Otherwise a name built round "ad" is cut only round ad furniture: no
	// text but labels, and no picture, video, frame or control of the page's
	// own — a classifieds listing's photo is called "ad-…" too.
	if f.content > 0 || f.img || f.media || f.heavy {
		return false, false
	}
	switch {
	case f.signal != webAdNone:
		return true, false
	case f.ads > 0:
		// A wrapper of ads and their labels: the frame round an ad.
		return f.near <= webAdWrapDepth && f.elems <= webAdWrapElems, false
	case !f.label || (!f.long && !webAdShortLabelTag(f.tag)):
		return false, false
	case f.block:
		return true, false
	}
	return false, true
}

// output builds the markup without the cut stretches: an element leaves the
// marker walk notes, a label's text run leaves nothing.
func (r *webAdRemover) output() (string, int) {
	if len(r.cuts) == 0 {
		return r.src, 0
	}
	cuts := r.cuts
	sort.Slice(cuts, func(i, j int) bool {
		if cuts[i].start != cuts[j].start {
			return cuts[i].start < cuts[j].start
		}
		return cuts[i].end > cuts[j].end
	})
	var b strings.Builder
	b.Grow(len(r.src))
	pos, count := 0, 0
	for _, c := range cuts {
		if c.start < pos || c.end <= c.start {
			// Inside a stretch already cut.
			continue
		}
		b.WriteString(r.src[pos:c.start])
		if c.element {
			b.WriteString(webAdCutMarker)
			count++
		}
		pos = c.end
	}
	b.WriteString(r.src[pos:])
	return b.String(), count
}

// open starts an element's frame and reads what its tag, names, attributes and
// address say about it. inner is a raw element's body (a <video>'s <source>).
func (r *webAdRemover) open(name, attrs string, start int, inner string) webAdFrame {
	f := webAdFrame{tag: name, start: start, block: webAdBlockTag(name)}
	switch name {
	case "html", "head", "body", "main":
		f.keep, f.hold = true, true
	case "td", "th", "thead", "tbody", "tfoot", "caption", "colgroup", "col",
		"option", "optgroup", "select", "datalist", "textarea":
		f.keep = true
	}
	if r.inHead || name == "head" {
		f.keep, f.hold = true, true
		return f
	}
	switch name {
	case "select", "textarea", "canvas", "audio":
		f.media = true
	}
	signal := webAdTagSignal(name)
	var src, dataSrc, data, href, width, height, title, frameName, id, typ string
	webAdEachAttr(attrs, func(key, value string) {
		switch key {
		case "class":
			for _, class := range strings.Fields(value) {
				signal = max(signal, webAdNameSignal(class))
			}
		case "id":
			id = value
			signal = max(signal, webAdNameSignal(strings.TrimSpace(value)))
		case "role":
			if strings.EqualFold(strings.TrimSpace(value), "main") {
				f.keep, f.hold = true, true
			}
		case "aria-label", "aria-roledescription", "data-label":
			signal = max(signal, webAdLabelSignal(value))
		case "data-testid", "data-test", "data-test-id", "data-qa", "data-cy", "data-e2e", "data-component",
			"data-component-type", "data-module", "data-type", "data-content-type", "data-block-type", "data-element":
			signal = max(signal, webAdNameSignal(strings.TrimSpace(value)))
		case "style":
			// A picture painted as a background is content like an <img>.
			if strings.Contains(asciiLower(value), "url(") {
				f.img = true
			}
		case "src":
			src = value
		case "data-src":
			dataSrc = value
		case "data":
			data = value
		case "href":
			href = value
		case "width":
			width = value
		case "height":
			height = value
		case "title":
			title = value
		case "name":
			frameName = value
		case "type":
			typ = value
		default:
			if strings.HasPrefix(key, "data-") {
				signal = max(signal, webAdDataSignal(key, value))
			}
		}
	})
	switch name {
	case "img":
		// A lazy picture's real address may wait in data-src behind a
		// placeholder src.
		if webAdPixel(width, height) || r.adAddress(src) || r.adAddress(dataSrc) {
			signal = webAdStrong
		} else {
			f.img = true
		}
	case "svg":
		// An icon is furniture; a drawing the size of a picture is content.
		if w, ok := webAdLength(width); ok && w > 64 {
			f.img = true
		} else if h, ok := webAdLength(height); ok && h > 64 {
			f.img = true
		}
	case "a":
		if r.adClick(href) {
			signal = webAdStrong
		}
	case "iframe":
		switch {
		case webAdFrameName(id) || webAdFrameName(frameName) || webAdFrameTitle(title) ||
			r.adAddress(src) || r.adAddress(dataSrc):
			signal = webAdStrong
		case webAdBlankAddress(src) && webAdBlankAddress(dataSrc):
			// An empty frame is ad scaffolding, neither an ad nor content.
		default:
			f.media = true
		}
	case "video":
		if r.adAddress(webVideoSrcFromElement(attrs, inner)) {
			signal = webAdStrong
		} else {
			f.media = true
		}
	case "object", "embed":
		addr := src
		if name == "object" {
			addr = data
		}
		if r.adAddress(addr) {
			signal = webAdStrong
		} else {
			f.media = true
		}
	case "input":
		if !strings.EqualFold(strings.TrimSpace(typ), "hidden") {
			f.media = true
		}
	}
	f.signal = signal
	return f
}

// resolve turns an attribute's address into an absolute http(s) URL, or nil.
func (r *webAdRemover) resolve(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" || r.base == nil || raw[0] == '#' {
		return nil
	}
	if strings.IndexByte(raw, '&') >= 0 {
		raw = htmlstd.UnescapeString(raw)
	}
	u, err := r.base.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	return u
}

// adAddress reports whether the request filter would refuse the address: a
// picture, video or frame from an ad server can only ever be an ad, or a blank
// placeholder where one was.
func (r *webAdRemover) adAddress(raw string) bool {
	u := r.resolve(raw)
	return u != nil && brwAdBlocks(r.page, u.String())
}

// adClick reports whether a link goes through an ad click server. Only an
// absolute address can name another server; a relative one is checked for
// the ad click paths alone.
func (r *webAdRemover) adClick(href string) bool {
	if href == "" {
		return false
	}
	lower := asciiLower(href)
	if !strings.Contains(lower, "//") && !strings.Contains(lower, "aclk") && !strings.Contains(lower, "ad_domain") &&
		!strings.Contains(lower, "ad_provider") {
		return false
	}
	u := r.resolve(href)
	return u != nil && webAdClickURL(r.page, u)
}

// webAdClickHosts are servers whose links are ad clicks: a link into one is an
// ad's own, not a page an article points at. A host matches with its
// subdomains, and never on its own site's pages.
var webAdClickHosts = webAdSet(
	"doubleclick.net", "googleadservices.com", "googlesyndication.com", "2mdn.net", "dartsearch.net",
	"adnxs.com", "adsrvr.org", "amazon-adsystem.com", "criteo.com", "criteo.net", "adform.net",
	"smartadserver.com", "serving-sys.com", "flashtalking.com", "adroll.com", "media.net", "yieldmo.com",
	"teads.tv", "sharethrough.com", "gumgum.com", "taboola.com", "outbrain.com", "revcontent.com",
	"mgid.com", "zergnet.com", "dianomi.com", "content.ad", "nativo.com", "ntv.io", "adblade.com",
	"propellerads.com", "popads.net", "exoclick.com", "adsterra.com", "trafficjunky.net", "juicyads.com",
	"clickadu.com", "adcash.com", "bidvertiser.com", "infolinks.com",
)

// webAdClickURL reports whether u is an ad's click-through: an ad click path
// (/aclk on Google and Bing, DuckDuckGo's ad_domain redirect) or a listed ad
// server that is not the page's own site.
func webAdClickURL(pageURL string, u *url.URL) bool {
	path, query := asciiLower(u.Path), asciiLower(u.RawQuery)
	if strings.Contains(path, "/aclk") || strings.Contains(query, "ad_domain=") || strings.Contains(query, "ad_provider=") {
		return true
	}
	host := asciiLower(u.Hostname())
	for h := host; h != ""; {
		if webAdClickHosts[h] {
			return !brwAdSameSite(pageURL, host)
		}
		dot := strings.IndexByte(h, '.')
		if dot < 0 {
			break
		}
		h = h[dot+1:]
	}
	return false
}

// webAdBlankAddress reports an address that loads nothing: none, about:blank,
// a javascript: stub.
func webAdBlankAddress(raw string) bool {
	lower := asciiLower(strings.TrimSpace(raw))
	return lower == "" || strings.HasPrefix(lower, "about:") || strings.HasPrefix(lower, "javascript:")
}

// webAdFrameName reports the ids and names Google gives its ad frames.
func webAdFrameName(v string) bool {
	lower := asciiLower(strings.TrimSpace(v))
	return strings.HasPrefix(lower, "google_ads_") || strings.HasPrefix(lower, "aswift_") ||
		strings.HasPrefix(lower, "google_esf")
}

// webAdFrameTitle reports a frame titled as an ad ("3rd party ad content",
// "Advertisement").
func webAdFrameTitle(v string) bool {
	if v == "" || len(v) > webAdLabelBytes {
		return false
	}
	norm := " " + webAdNormalize(htmlstd.UnescapeString(v)) + " "
	return strings.Contains(norm, " advertisement ") || strings.Contains(norm, " ad content ") ||
		strings.Contains(norm, " sponsored ") || webAdIsLabel(strings.TrimSpace(norm))
}

// webAdPixel reports a picture sized 1×1 or smaller: a tracking pixel.
func webAdPixel(width, height string) bool {
	w, okW := webAdLength(width)
	h, okH := webAdLength(height)
	return okW && okH && w <= 1 && h <= 1
}

// webAdLength reads a width or height attribute in pixels.
func webAdLength(v string) (float64, bool) {
	v = strings.TrimSuffix(asciiLower(strings.TrimSpace(v)), "px")
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(v, 64)
	return n, err == nil
}

// webAdEachAttr calls fn with each attribute of a start tag's attribute text,
// tokenised as webParseAttrs does: the name lower-cased, the value raw.
func webAdEachAttr(raw string, fn func(name, value string)) {
	for i, n := 0, 0; i < len(raw) && n < webMaxElementAttrs; {
		for i < len(raw) && (webAttrSpaceByte(raw[i]) || raw[i] == '/') {
			i++
		}
		start := i
		for i < len(raw) && raw[i] != '=' && raw[i] != '/' && raw[i] != '>' && !webAttrSpaceByte(raw[i]) {
			i++
		}
		if i == start && i < len(raw) && raw[i] != '=' {
			i++
			continue
		}
		name := asciiLower(raw[start:i])
		for i < len(raw) && webAttrSpaceByte(raw[i]) {
			i++
		}
		value := ""
		if i < len(raw) && raw[i] == '=' {
			i++
			for i < len(raw) && webAttrSpaceByte(raw[i]) {
				i++
			}
			if i < len(raw) && (raw[i] == '"' || raw[i] == '\'') {
				quote := raw[i]
				i++
				valueStart := i
				for i < len(raw) && raw[i] != quote {
					i++
				}
				value = raw[valueStart:i]
				if i < len(raw) {
					i++
				}
			} else {
				valueStart := i
				for i < len(raw) && raw[i] != '>' && !webAttrSpaceByte(raw[i]) {
					i++
				}
				value = raw[valueStart:i]
			}
		}
		if name != "" {
			fn(name, value)
			n++
		}
	}
}

// webAdBlockTag reports a block-level element: a label alone in one stands on
// a line of its own.
func webAdBlockTag(tag string) bool {
	return webBlockElement(tag) || tag == "ul" || tag == "ol"
}

// webAdShortLabelTag reports the elements a bare "Ad" may label on its own. A
// link, a heading or a list item reading "Ads" is more likely a menu entry.
func webAdShortLabelTag(tag string) bool {
	switch tag {
	case "div", "span", "p", "small", "b", "strong", "em", "i", "label", "figcaption", "aside",
		"section", "ins", "mark", "font", "abbr", "sup", "sub", "center", "header", "footer":
		return true
	}
	return false
}

// webAdTagSignal reads a tag name: AMP's ad elements, and a custom element
// named for an ad (<shreddit-ad-post>, <gpt-ad>).
func webAdTagSignal(tag string) uint8 {
	switch tag {
	case "amp-ad", "amp-embed", "amp-sticky-ad", "amp-auto-ads":
		return webAdStrong
	}
	if strings.IndexByte(tag, '-') > 0 {
		if s := webAdNameSignal(tag); s != webAdNone {
			return max(s, webAdExplicit)
		}
	}
	return webAdNone
}

// webAdLabelSignal reads an accessible name: an element announced as
// "Advertisement" or "Sponsored" is one.
func webAdLabelSignal(value string) uint8 {
	switch _, kind := webAdTextKind(value); kind {
	case webAdTextLabel:
		return webAdExplicit
	case webAdTextShort:
		return webAdGeneric
	}
	return webAdNone
}

// webAdDataAttrs are data attributes only ad code writes.
var webAdDataAttrs = webAdSet(
	"data-ad-client", "data-ad-slot", "data-ad-format", "data-ad-layout-key", "data-adsbygoogle-status",
	"data-google-query-id", "data-google-container-id", "data-is-safeframe", "data-ad-unit-path",
	"data-text-ad", "data-ob-template",
)

// webAdDataSignal reads a data attribute's name (data-ad-slot, data-ad-name,
// data-sponsored). A flag set to false says the opposite.
func webAdDataSignal(key, value string) uint8 {
	if webAdDataAttrs[key] {
		return webAdStrong
	}
	rest := key[len("data-"):]
	if !strings.Contains(rest, "ad") && !strings.Contains(rest, "sponsor") {
		return webAdNone
	}
	switch asciiLower(strings.TrimSpace(value)) {
	case "false", "0", "no", "off", "none", "null":
		return webAdNone
	}
	return webAdNameSignal(rest)
}

// webAdNameSignal reads one class name, id or test name. A name is split into
// word groups at punctuation ("story-ad__slot") and a group into words at its
// camel-case humps ("topAdSlot"); it marks an ad when one word is an ad word
// and the rest are the words ad slots are named with (slot, wrapper, top,
// 300x250…), allowing one word of the site's own ("nytimes-ad"). A word that
// names a listing's parts or a page state clears it ("ad-title", "has-ads",
// "ad-free"): on a classifieds site an ad is a listing.
func webAdNameSignal(name string) uint8 {
	if len(name) < 2 || len(name) > 96 {
		return webAdNone
	}
	lower := asciiLower(name)
	if s, ok := webAdNames[lower]; ok {
		return s
	}
	for _, prefix := range webAdStrongPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return webAdStrong
		}
	}
	if !webAdMayMark(lower) {
		return webAdNone
	}
	// A BEM modifier says what the block is: result--ad, card--sponsored.
	if i := strings.LastIndex(lower, "--"); i >= 0 {
		if s := webAdModifiers[lower[i+2:]]; s != webAdNone {
			return s
		}
	}
	found, foreign := webAdNone, 0
	for i := 0; i < len(name); {
		if !webAdAlnum(name[i]) {
			i++
			continue
		}
		j := i
		for j < len(name) && webAdAlnum(name[j]) {
			j++
		}
		s, kind := webAdPart(name[i:j], lower[i:j])
		i = j
		switch kind {
		case webAdPartExcluded:
			return webAdNone
		case webAdPartAd:
			if s == webAdStrong {
				return s
			}
			found = max(found, s)
		case webAdPartForeign:
			foreign++
		}
	}
	if foreign > 1 {
		return webAdNone
	}
	return found
}

// webAdPart sorts one word group of a name (part, and lower, its lower-case
// copy): an ad word with its strength, a word ad slots are named with, a word
// that clears the name, or a word of the site's own.
func webAdPart(part, lower string) (uint8, int) {
	if s, ok := webAdWords[lower]; ok {
		return s, webAdPartAd
	}
	if webAdExcluded[lower] {
		return webAdNone, webAdPartExcluded
	}
	if s := webAdGlued(lower); s != webAdNone {
		return s, webAdPartAd
	}
	if webAdCompanion(lower, true) {
		return webAdNone, webAdPartCompanion
	}
	if webAdHumped(part) {
		return webAdCamel(part, lower)
	}
	return webAdNone, webAdPartForeign
}

// webAdCamel sorts a camel-case word group ("topAdSlot", "DFPAdUnit") by its
// words. A group of random letters — a style hash like "adQBCa" — has words
// no list knows, and marks nothing.
func webAdCamel(part, lower string) (uint8, int) {
	found, kind := webAdNone, webAdPartCompanion
	start := 0
	word := func(end int) {
		w := lower[start:end]
		start = end
		switch {
		case kind == webAdPartExcluded:
		case webAdExcluded[w]:
			kind = webAdPartExcluded
		case webAdWords[w] != webAdNone:
			found = max(found, webAdWords[w])
		case webAdCompanion(w, false):
		default:
			kind = webAdPartForeign
		}
	}
	for i := 1; i < len(part); i++ {
		a, b := part[i-1], part[i]
		switch {
		case webAdLowerByte(a) && webAdUpperByte(b),
			webAdDigitByte(a) != webAdDigitByte(b),
			webAdUpperByte(a) && webAdUpperByte(b) && i+1 < len(part) && webAdLowerByte(part[i+1]):
			word(i)
		}
	}
	word(len(part))
	switch {
	case kind == webAdPartExcluded:
		return webAdNone, webAdPartExcluded
	case kind == webAdPartForeign:
		return webAdNone, webAdPartForeign
	case found != webAdNone:
		return found, webAdPartAd
	}
	return webAdNone, webAdPartCompanion
}

// webAdGlued reads a word group that glues "ad" to a slot word: adslot,
// adsbox, topad, sidebarads, ad300x250.
func webAdGlued(lower string) uint8 {
	for _, ad := range [...]string{"ads", "ad"} {
		if rest, ok := strings.CutPrefix(lower, ad); ok && rest != "" && webAdCompanion(rest, false) {
			return webAdGeneric
		}
		if rest, ok := strings.CutSuffix(lower, ad); ok && rest != "" && webAdCompanions[rest] {
			return webAdGeneric
		}
	}
	return webAdNone
}

// webAdCompanion reports a word ad slots are named with: a listed one, a
// number or a size (2, 300x250), or — between punctuation only, where it is a
// namespace like "mv-" or "js-" — any short word. A hash ("3f9c") is none of
// these.
func webAdCompanion(lower string, short bool) bool {
	if webAdCompanions[lower] {
		return true
	}
	digits, numberish := false, true
	for i := 0; i < len(lower); i++ {
		switch c := lower[i]; {
		case webAdDigitByte(c):
			digits = true
		case c != 'x':
			numberish = false
		}
	}
	if digits && numberish {
		return true
	}
	return short && len(lower) <= 3
}

// webAdMayMark is a quick test that a lower-case name could mark an ad at all.
func webAdMayMark(lower string) bool {
	for _, hint := range webAdHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func webAdAlnum(c byte) bool {
	return webAdLowerByte(c) || webAdUpperByte(c) || webAdDigitByte(c)
}

func webAdLowerByte(c byte) bool { return 'a' <= c && c <= 'z' }

func webAdUpperByte(c byte) bool { return 'A' <= c && c <= 'Z' }

func webAdDigitByte(c byte) bool { return '0' <= c && c <= '9' }

// webAdHumped reports a camel-case word group: upper- and lower-case letters
// both.
func webAdHumped(part string) bool {
	upper, lower := false, false
	for i := 0; i < len(part); i++ {
		upper = upper || webAdUpperByte(part[i])
		lower = lower || webAdLowerByte(part[i])
	}
	return upper && lower
}

// webAdTextKind reads a text run: how many letters and digits it has, and
// whether it is blank, an ad label or content.
func webAdTextKind(s string) (int, int) {
	if len(s) <= webAdLabelBytes && strings.IndexByte(s, '&') >= 0 {
		s = htmlstd.UnescapeString(s)
	}
	letters := webAdLetters(s)
	switch {
	case letters == 0:
		return 0, webAdTextBlank
	case letters > webAdLabelLetters || len(s) > webAdLabelBytes:
		return letters, webAdTextContent
	}
	norm := webAdNormalize(s)
	switch {
	case norm == "ad" || norm == "ads":
		return letters, webAdTextShort
	case webAdIsLabel(norm):
		return letters, webAdTextLabel
	case webAdNeutral[norm]:
		return letters, webAdTextBlank
	}
	return letters, webAdTextContent
}

// webAdLetters counts the letters and digits of s.
func webAdLetters(s string) int {
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if webAdAlnum(c) {
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
		i += size
	}
	return n
}

// webAdNormalize lower-cases s and keeps only its words, one space apart:
// "ADVERTISEMENT - Scroll to continue" → "advertisement scroll to continue".
func webAdNormalize(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			gap = true
			continue
		}
		if gap && b.Len() > 0 {
			b.WriteByte(' ')
		}
		gap = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// webAdIsLabel reports a normalized text run that labels an ad.
func webAdIsLabel(norm string) bool {
	if webAdLabels[norm] {
		return true
	}
	n := len(norm)
	switch {
	case strings.HasPrefix(norm, "advertisement ") && n <= 64,
		strings.HasPrefix(norm, "sponsored by ") && n <= 48,
		strings.HasPrefix(norm, "promoted by ") && n <= 48,
		strings.HasPrefix(norm, "paid for by ") && n <= 64,
		strings.HasPrefix(norm, "ads by ") && n <= 32:
		return true
	}
	if n <= 64 && (strings.Contains(norm, "continues below") || strings.Contains(norm, "continues after") ||
		strings.Contains(norm, "continue reading below")) {
		return strings.Contains(norm, "advertisement") || strings.Contains(norm, " ad ") ||
			strings.HasSuffix(norm, " ad") || strings.HasSuffix(norm, " ads")
	}
	return false
}

// webAdSet builds a lookup set.
func webAdSet(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}

// webAdWords are the words of a name that mark an ad, by strength.
var webAdWords = func() map[string]uint8 {
	words := map[string]uint8{}
	for _, w := range []string{
		"ad", "ads", "advert", "adverts", "advertising", "advertiser", "advertisers", "promoted",
		"mpu", "skyscraper", "adbox", "adspace", "adzone", "adframe", "adbanner", "adarea",
		"adlabel", "adchoices", "adhesion", "nativead", "nativeads",
		// The same in the languages sites name their boxes in: only ever
		// round ad furniture, since "anzeige" is a classifieds listing too.
		"werbung", "anzeige", "anzeigen", "reklame", "reklama", "publicite", "publicidad",
		"pubblicita", "publicidade", "advertentie",
	} {
		words[w] = webAdGeneric
	}
	for _, w := range []string{
		"advertisement", "advertisements", "advertisment", "advertisments", "advertorial",
		"advertorials", "sponsored", "sponsoredcontent", "sponsoredlinks", "promotedlink",
		"promotedcontent", "adslot", "adslots", "adunit", "adunits", "adcontainer", "adwrapper",
		"adplaceholder", "adholder", "dfp",
	} {
		words[w] = webAdExplicit
	}
	for _, w := range []string{
		"adsbygoogle", "adsense", "doubleclick", "googleads", "googlead", "taboola", "outbrain",
		"dianomi", "zergnet", "revcontent", "mgid", "adthrive", "raptive", "ezoic", "advads",
		"adsanity", "adrotate", "carbonads", "buysellads", "teads", "connatix", "primis",
		"freestar", "adngin", "playwire", "nitropay", "criteo", "prebid", "anyclip",
	} {
		words[w] = webAdStrong
	}
	return words
}()

// webAdNames are whole names that mark an ad: ad-tech classes and the search
// engines' ad blocks (Google, Bing, Yahoo, DuckDuckGo, old Reddit).
var webAdNames = func() map[string]uint8 {
	names := map[string]uint8{}
	for _, n := range []string{
		"adsbygoogle", "tads", "tadsb", "bottomads", "tvcap", "ads-ad", "commercial-unit-desktop-top",
		"commercial-unit-desktop-rhs", "googleactiveviewelement", "googleactiveviewinnercontainer",
		"ob-widget", "ob-smartfeed-wrapper", "b_ad", "b_adtop", "b_adbottom", "b_admiddle",
		"searchcentertopads", "searchcenterbottomads", "searchrighttopads", "searchrightbottomads",
		"result--ad", "result-sponsored", "promotedlink",
	} {
		names[n] = webAdStrong
	}
	for _, n := range []string{"is-ad", "isad", "is-ads", "is-sponsored", "issponsored", "is-advertisement"} {
		names[n] = webAdExplicit
	}
	return names
}()

// webAdStrongPrefixes begin ad-tech names: GPT slots and frames, Taboola's
// trc_ containers, ad networks' widgets.
var webAdStrongPrefixes = []string{
	"div-gpt-ad", "google_ads_", "gpt-ad-", "aswift_", "trc_", "taboola", "outbrain", "ezoic",
	"adthrive", "advads", "zergnet", "dianomi", "revcontent", "mgid", "criteo", "doubleclick",
	"adsense", "amzn-assoc", "amzn_assoc", "connatix", "freestar", "carbonads",
}

// webAdModifiers are BEM modifiers that say the block is an ad.
var webAdModifiers = map[string]uint8{
	"ad": webAdExplicit, "ads": webAdExplicit, "advert": webAdExplicit, "advertisement": webAdExplicit,
	"sponsored": webAdExplicit, "advertorial": webAdExplicit, "promoted": webAdGeneric,
}

// webAdHints are substrings every name that can mark an ad contains.
var webAdHints = []string{
	"ad", "sponsor", "promot", "mpu", "skyscraper", "dfp", "taboola", "outbrain", "dianomi",
	"zergnet", "revcontent", "mgid", "ezoic", "criteo", "primis", "connatix", "freestar",
	"playwire", "nitropay", "prebid", "doubleclick", "raptive", "anyclip", "werbung", "anzeige",
	"reklam", "publici", "pubblicit",
}

// webAdExcluded are words that clear a name: a page state ("has-ads",
// "ad-free", "adblock-notice"), the parts of a classifieds listing ("ad-title",
// "ad-price", "ad-photo"), an ads dashboard or policy page, icon fonts and
// country flags (the Andorra flag is "fi-ad").
var webAdExcluded = webAdSet(
	"has", "no", "non", "not", "with", "without", "free", "enabled", "enable", "disabled", "disable",
	"allowed", "allow", "off", "lite", "hidden", "toggle", "consent", "blocked", "blocker", "blockers",
	"blocking", "adblock", "adblocker", "adblocking", "detect", "detected", "detector", "detection",
	"debug", "title", "titles", "price", "prices", "listing", "listings", "description", "desc",
	"details", "detail", "seller", "sellers", "location", "date", "photo", "photos", "image", "images",
	"img", "thumb", "thumbs", "thumbnail", "thumbnails", "gallery", "pic", "pics", "picture",
	"pictures", "info", "meta", "id", "ids", "number", "num", "page", "pages", "view", "views",
	"filter", "filters", "category", "categories", "cat", "form", "create", "edit", "new", "my",
	"user", "users", "owner", "contact", "phone", "reply", "message", "messages", "favorite",
	"favourite", "favorites", "favourites", "fav", "favs", "heart", "like", "likes", "star", "stars",
	"bookmark", "save", "saved", "share", "follow", "watch", "link", "links", "url", "urls", "click",
	"clicks", "manage", "management", "manager", "settings", "setting", "preferences", "policy",
	"policies", "terms", "term", "help", "faq", "account", "dashboard", "campaign", "campaigns",
	"creator", "builder", "editor", "stats", "insights", "analytics", "performance", "billing",
	"payment", "budget", "audience", "targeting", "keyword", "keywords", "config", "param", "params",
	"fa", "fas", "far", "fab", "fal", "icon", "icons", "bi", "glyphicon", "material", "flag", "flags",
	"fi", "country", "countries", "lang", "language", "locale", "hoc", "adhoc", "tag", "tags",
	"topic", "topics", "taxonomy", "author", "avatar", "comment", "comments", "review", "reviews",
	"rating", "ratings",
)

// webAdCompanions are the words ad slots are named with: where, how big,
// what kind of box, which ad stack.
var webAdCompanions = webAdSet(
	"slot", "slots", "unit", "units", "container", "containers", "wrapper", "wrappers", "wrap", "box",
	"boxes", "banner", "banners", "space", "spaces", "zone", "zones", "label", "labels",
	"placeholder", "placeholders", "frame", "frames", "holder", "holders", "block", "blocks", "area",
	"areas", "region", "regions", "position", "positions", "pos", "inline", "display", "sticky",
	"top", "bottom", "side", "sidebar", "rail", "right", "left", "center", "centre", "middle", "mid",
	"leaderboard", "native", "google", "mobile", "desktop", "tablet", "header", "footer", "inner",
	"outer", "content", "contents", "body", "text", "notice", "feedback", "loader", "loading", "lazy",
	"article", "story", "stories", "feed", "list", "grid", "row", "rows", "col", "cols", "column",
	"columns", "section", "sections", "billboard", "sky", "rectangle", "rect", "halfpage",
	"interstitial", "overlay", "video", "player", "iframe", "refresh", "sizer", "spacer", "marker",
	"choices", "choice", "wide", "small", "large", "big", "medium", "first", "second", "third",
	"item", "items", "card", "cards", "result", "component", "module", "widget", "widgets",
	"element", "host", "target", "anchor", "fixed", "float", "floating", "corner", "popup", "modal",
	"flyout", "expand", "expandable", "teaser", "promo", "partner", "partners", "commercial",
	"takeover", "skin", "wallpaper", "background", "badge", "indicator", "attribution",
	"disclosure", "disclaimer", "tagline", "creative", "creatives", "slug", "close", "dismiss",
	"report", "why", "name", "type", "size", "sizes", "path", "placement", "placements", "format",
	"layout", "status", "injected", "inject", "injection", "dynamic", "async", "lazyload", "render",
	"rendered", "gpt", "bidder", "house", "internal", "external", "party", "safeframe",
	"responsive", "flex", "before", "after", "home", "homepage", "front", "main", "vertical",
	"horizontal", "square", "leader", "bar", "strip", "panel", "panels", "tile", "tiles", "rotator",
	"carousel", "slider", "sponsor", "sponsors", "primary", "secondary", "upper", "lower", "global",
	"site", "sitewide", "mob", "amp", "web", "loaded", "empty", "filled", "visible", "active",
	"ready", "init", "post", "js", "div", "outstream", "instream", "dfp", "gam", "rtb", "hb",
)

// webAdLabels are the normalized label lines that announce an ad.
var webAdLabels = webAdSet(
	"advertisement", "advertisements", "advertisment", "advertisments", "advertisement feature",
	"advertiser", "advertiser content", "sponsored", "sponsored content", "sponsored contents",
	"sponsored links", "sponsored link", "sponsored post", "sponsored posts", "sponsored stories",
	"sponsored story", "sponsored article", "sponsored articles", "sponsored results",
	"sponsored result", "sponsored listing", "sponsored listings", "sponsored products",
	"sponsored product", "sponsored ad", "sponsored ads", "promoted", "promoted content",
	"promoted stories", "promoted links", "promoted post", "promoted posts", "paid content",
	"paid post", "paid partnership", "paid promotion", "from our advertisers", "from our sponsors",
	"ad feedback", "report this ad", "report ad", "close ad", "hide ad", "skip ad",
	"skip advertisement", "why this ad", "adchoices", "ad choices", "ads by google",
	"advertise here", "your ad here", "scroll to continue with content",
	// German, French, Spanish, Italian, Portuguese, Dutch, Nordic, Polish,
	// Czech, Hungarian, Romanian, Russian, Turkish, Arabic, Hebrew, Hindi,
	// Vietnamese, Indonesian, Thai, Chinese, Japanese, Korean.
	"anzeige", "werbung", "gesponsert", "publicité", "publicite", "sponsorisé", "sponsorise",
	"contenu sponsorisé", "publicidad", "patrocinado", "contenido patrocinado", "anuncio",
	"pubblicità", "pubblicita", "sponsorizzato", "contenuto sponsorizzato", "publicidade",
	"advertentie", "advertenties", "gesponsord", "annons", "annonse", "sponsrad", "reklame",
	"reklama", "reklamy", "sponsorowane", "mainos", "hirdetés", "reclamă", "реклама", "reklam",
	"إعلان", "פרסומת", "विज्ञापन", "quảng cáo", "iklan", "โฆษณา", "广告", "廣告", "広告", "광고",
	"スポンサー", "스폰서",
)

// webAdNeutral are the words of an ad's own controls: neither content nor a
// label.
var webAdNeutral = webAdSet("x", "close", "dismiss", "hide", "skip", "report", "i", "info")
