package main

// webpage.go — styled HTML rendering for the built-in browser.
//
// A stock TrimUI Smart Pro has no WebKit/Chromium, no window manager and 1 GB
// of shared RAM, so embedding a browser engine is not an option. Instead this
// reader parses the page's HTML5 markup itself, resolves the part of CSS that
// decides how text looks (colour, size, weight, alignment, spacing,
// backgrounds, lists, quotes and code blocks) and draws the result with the
// app's own SDL/TTF pipeline.
//
// The parser is streaming: it never keeps a DOM, only the styled text blocks it
// is about to draw, and it stops at fixed byte/block limits. A styled page
// therefore costs a few hundred kilobytes on a 1 GB handheld instead of the
// tens of megabytes a real engine would need.

import (
	"context"
	"fmt"
	htmlstd "html"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// webMaxTextBytes bounds how much readable text one page may contribute.
	webMaxTextBytes = 384 << 10
	// webMaxBlocks bounds the block list so a pathological page cannot make
	// the layout take seconds on the handheld.
	webMaxBlocks = 1800
	// webMaxStylesheets and webSheetByteCap bound external CSS fetching: a
	// handful of small sheets is what makes real sites look like themselves.
	webMaxStylesheets = 3
	webSheetByteCap   = 256 << 10
	webMaxCSSBytes    = 512 << 10
	// webMaxRows bounds the laid-out row count (each row is one text line).
	webMaxRows = 12000
)

// webElement is the little of an open element the CSS matcher needs.
type webElement struct {
	tag     string
	id      string
	classes []string
}

// The layout enums are small integers rather than strings: they are copied into
// every block and compared while laying out, and the reader runs on a handheld.
const (
	webDisplayBlock = iota
	webDisplayFlex
)

const (
	webFlexRow = iota
	webFlexColumn
)

const (
	webPositionStatic = iota
	webPositionRelative
	webPositionAbsolute
	webPositionFixed
)

const (
	webItemStart = iota
	webItemCenter
	webItemEnd
	webItemStretch
)

const (
	webJustifyStart = iota
	webJustifyCenter
	webJustifyEnd
	webJustifyBetween
	webJustifyAround
)

// webStyle is a resolved style for one element. Zero values are meaningful:
// HasColor/HasBg distinguish "author asked for this" from "inherit".
type webStyle struct {
	Color      sdl.Color
	HasColor   bool
	Background sdl.Color
	HasBg      bool
	// Gradient is the ramp a background-image gradient resolved to. HasBg is
	// still set (to the ramp's average) so contrast, fade and inheritance keep
	// working with one colour; the ramp wins where the reader paints. It is a
	// pointer only so a style stays comparable (runs merge on style equality)
	// and so a hundred runs can share one parsed ramp.
	Gradient    *webGradient
	HasGradient bool
	FontPx      float64
	Bold        bool
	Italic      bool
	Underline   bool
	Strike      bool
	Link        bool
	Align       int
	Pre         bool
	Hidden      bool
	Indent      float64
	TextIndent  float64
	MarginTop   float64
	MarginBot   float64
	// Transform is the author's text-transform (0 none, 1 uppercase,
	// 2 lowercase, 3 capitalise). It is applied when a word is measured so
	// the wrapped width matches the glyphs that get drawn.
	Transform int
	// Borders are reported separately: a rule under a heading and a card's
	// full outline are drawn differently from a quote's left bar.
	BorderLeft     bool
	BorderRight    bool
	BoxBorder      bool
	BorderTop      bool
	BorderBottom   bool
	BorderColor    sdl.Color
	HasBorderColor bool
	// BorderWidth is the widest declared border in CSS px; 0 keeps the
	// reader's own default (3 for a side bar, 2 for a rule or an outline).
	// A card written as `border: 3px` used to draw 2px, which reads as a
	// different design rather than as a smaller border.
	BorderWidth float64
	ListNone    bool
	// ListStyle is the marker the author asked for (0 = the reader default).
	// It inherits like a real browser's, so `ul { list-style: none }` reaches
	// the items, which is how nav menus hide their bullets.
	ListStyle int
	// LineHeight is the author's line-height in CSS px; 0 leaves the reader's
	// row grid in charge. It inherits, so `body { line-height: 1.6 }` spaces
	// every paragraph the way the design intended.
	LineHeight float64
	// Radius is border-radius in CSS px; 0 keeps the reader's crisp corners.
	// A percentage (or a large value) is clamped to half the box at draw time.
	Radius float64
	// HasOpacity/Opacity carry the element's alpha. It is applied when text,
	// fills and borders are painted, by blending toward whatever is behind
	// them; children inherit the fade, like a real opacity group.
	HasOpacity bool
	Opacity    float64
	// IndentRight is padding-right + margin-right in CSS px: it narrows the
	// measure so an inset card wraps where the author intended.
	IndentRight float64
	// --- Flex and stacking -------------------------------------------------
	// Display is block or flex. A flex container lays its direct children out
	// side by side (row) or one under the other (column); the reader has no
	// DOM, so the parser marks each direct child as one flex item and the
	// layout stacks those items into columns.
	Display    int
	FlexDir    int
	Justify    int
	AlignItems int
	GapX, GapY float64
	FlexWrap   bool
	// FlexGrow/Basis describe the element as a flex item. Basis is -1 when the
	// author did not state one (auto), which is different from 0px.
	FlexGrow  float64
	FlexBasis float64
	BasisPct  float64
	AlignSelf int
	// Width/WidthPct size a flex item explicitly, the way a card with
	// `width: 33%` or `width: 240px` does.
	Width    float64
	WidthPct float64
	// ZIndex and Position place an element in the stacking order. Absolute and
	// fixed elements leave the flow and are painted over the text in z order.
	ZIndex    int
	Position  int
	Top       float64
	Right     float64
	Bottom    float64
	Left      float64
	HasTop    bool
	HasRight  bool
	HasBottom bool
	HasLeft   bool
	// Href is the target of the anchor this text belongs to, empty for every
	// other text. Elements inside an <a> inherit it, so a bold or spanned
	// label still knows where it points and the browser can walk links with
	// the D-pad.
	Href string
	// Vars is the custom-property scope in force for this element. Every
	// var() is already resolved by the time a style reaches a block, so this
	// only carries inheritance down the parse; it stays a pointer so a
	// webStyle is still comparable and adjacent runs still merge.
	Vars *webVarScope
}

// webVarScope is one element's view of the custom properties: the ones it
// inherited plus the ones it declares, all expanded to plain text.
type webVarScope struct {
	props map[string]string
}

// lookup returns a property already expanded past var(). A nil scope is the
// document before any `--name` was seen.
func (s *webVarScope) lookup(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	value, ok := s.props[name]
	return value, ok
}

// webRun is a styled span inside a block.
type webRun struct {
	Text  string
	Style webStyle
}

// webBlock is one laid-out unit: a paragraph, heading, list item, quote, code
// block or horizontal rule.
type webBlock struct {
	Runs           []webRun
	Style          webStyle
	Align          int
	Indent         float64
	TextIndent     float64
	MarginTop      float64
	MarginBot      float64
	Bullet         string
	Pre            bool
	HasBg          bool
	Background     sdl.Color
	Gradient       webGradient
	HasGradient    bool
	BorderLeft     bool
	BorderRight    bool
	BoxBorder      bool
	BorderTop      bool
	BorderBottom   bool
	BorderColor    sdl.Color
	HasBorderColor bool
	BorderWidth    float64
	Rule           bool
	// FlexGroup/FlexItem tie a block to its flex container and direct child.
	// Zero means "not in a flex container". The container's own row settings
	// are copied onto every item so a sub-layout can be built without the
	// parser's element stack.
	FlexGroup   int
	FlexItem    int
	FlexDir     int
	FlexJustify int
	FlexAlign   int
	FlexGapX    float64
	FlexGapY    float64
	FlexWrap    bool
	FlexGrow    float64
	FlexBasis   float64
	BasisPct    float64
	Width       float64
	WidthPct    float64
	AlignSelf   int
	// Float marks an absolutely or fixed-positioned block: it is measured on
	// its own and painted over the flow in ZIndex order.
	Float     bool
	ZIndex    int
	PosTop    float64
	PosRight  float64
	PosBottom float64
	PosLeft   float64
	HasTop    bool
	HasRight  bool
	HasBottom bool
	HasLeft   bool
	// InMain marks text inside <main>/<article>. When a page has any, the
	// reader shows that content instead of the site's navigation furniture.
	InMain bool
	// Image marks an <img> block. ImageSrc is the resolved http(s) URL or a
	// data:image/...;base64 URI; ImageW/ImageH are the width/height attrs in
	// CSS px (zero when absent — the natural pixel size fills in at layout).
	Image    bool
	ImageSrc string
	ImageW   float64
	ImageH   float64
	// Video marks a <video> element or a video-bearing <iframe> embed.
	// VideoSrc is the resolved http(s) URL the playback path streams through
	// yt-dlp; the layout reserves a 16:9 play card for it like an image and
	// the browser lists it among the page's links so the D-pad reaches it.
	// VideoThumb is the video's real thumbnail URL when it can be derived
	// from the source alone (YouTube's image CDN serves one per video id);
	// the image pass fetches it and the card paints it behind the glyph.
	Video      bool
	VideoSrc   string
	VideoThumb string
	VideoTitle string
}

// webDocument is one parsed page. It holds no raw HTML — the parser keeps the
// styled blocks and the layout builds from those — plus the paper and text
// colours the page asked for.
type webDocument struct {
	URL    string
	Title  string
	Blocks []webBlock
	Paper  sdl.Color
	// PaperGradient is a body/html background ramp: the page under the text is
	// the one gradient that covers the whole panel, so it is painted too.
	PaperGradient    webGradient
	HasPaperGradient bool
	Text             sdl.Color

	// Images holds the fetched bytes of every <img> in the document, keyed by
	// resolved URL or data URI; ImageMeta records the natural pixel sizes the
	// layout needs. Set imageMu (the field below) guards these two maps.
	Images    map[string][]byte
	ImageMeta map[string]webImageMeta

	mu     sync.Mutex
	stamp  string
	layout *webLayout

	// imageMu guards Images and ImageMeta separately from mu. They need their
	// own lock because the layouter reads the natural sizes while layoutFor
	// holds mu: sharing one mutex deadlocked the reader on the first page with
	// an image whose size the markup did not give, which froze the whole app.
	imageMu sync.Mutex
}

// webRunPos is a measured run, ready to draw.
type webRunPos struct {
	Text      string
	X         int32
	Font      *ttf.Font
	Color     sdl.Color
	Bold      bool
	Underline bool
	Strike    bool
	// Href is set when this run is part of a link, so the browser can outline
	// and follow it without re-parsing the page.
	Href string
	// Inline backgrounds (a <code> chip inside a paragraph) are per run; a
	// block-level background is painted once for the whole block instead.
	HasBg bool
	Bg    sdl.Color
}

// webRow is one line slot in the uniform layout grid. Tall lines (headings)
// claim extra empty slots so nothing ever overlaps the next line.
type webRow struct {
	Y    int32
	Runs []webRunPos
}

// webRect is a background, rule or border drawn with (or behind) the text of
// a layout. Stroke selects an outline instead of a fill.
type webRect struct {
	Rect      sdl.Rect
	Color     sdl.Color
	Stroke    bool
	Thickness int32
	// Gradient paints this rect as a ramp instead of a flat fill; Color is then
	// the average, used for the corner rounding and as the fallback.
	Gradient    webGradient
	HasGradient bool
	// Radius is the author's border-radius in design pixels, clamped to half
	// the shorter side when drawn. 0 keeps the reader's crisp corner.
	Radius int32
	// Z orders the fills and borders of the page. Text is always painted after
	// every rect, so z-index decides which background or border wins where
	// two elements overlap.
	Z int
}

// webFloat is an absolutely positioned block: it is laid out at its own width,
// then painted over the flow in z order. A page's badge, sticky header or modal
// is exactly this.
type webFloat struct {
	Z      int
	X, Y   int32
	Rows   []webRow
	Rects  []webRect
	Images []webImagePos
}

// webLayout is the measured page for one width and font configuration.
type webLayout struct {
	Rows   []webRow
	Rects  []webRect
	RowH   int32
	Height int32
	// Links are the page's clickable runs in reading order, one entry per
	// destination. The browser walks this list with the D-pad.
	Links []webLinkHit
	// Floats are the absolutely positioned blocks, painted over the rows in
	// ascending z order.
	Floats []webFloat
	// Images are the reader's <img> boxes, in layout-local coordinates like
	// Rects. They draw after the icon rects and before the text rows.
	Images []webImagePos
	// Videos are the reader's <video>/embed play cards, in the same layout-
	// local coordinates. They draw right after the images, before the text.
	Videos []webVideoPos
}

// webLinkHit is one link on a laid-out page: the row it starts on and its
// pixel extent inside that row. Href is the raw attribute; the browser
// resolves it against the document URL when the link is opened.
type webLinkHit struct {
	Row  int
	X    int32
	W    int32
	Href string
	Text string
	// Video marks a hit generated from a <video> play card rather than an
	// anchor: activation must stream/play it instead of navigating to it.
	Video bool
}

// webRule is one parsed CSS rule (one selector; groups are split apart).
type webRule struct {
	selector    string
	specificity int
	order       int
	decls       map[string]string
	// important holds the !important declarations of the same rule. They are
	// kept apart because they outrank every normal declaration, whatever the
	// selector's specificity.
	important map[string]string
}

// webFrame is an open element during the streaming parse.
type webFrame struct {
	el    webElement
	style webStyle
	kind  string // "block", "inline", "list", "skip"
	// flexGroup is non-zero when this element opened a flex container. The
	// matching frame is popped when the element closes.
	flexGroup int
}

// webFlexFrame is one open flex container while parsing. item counts its
// direct children so every block can be tagged with the item it belongs to.
type webFlexFrame struct {
	group   int
	item    int
	depth   int
	dir     int
	justify int
	align   int
	gapX    float64
	gapY    float64
	wrap    bool
}

// webListState tracks <ul>/<ol> numbering.
type webListState struct {
	ordered bool
	n       int
}

// --- Document store -------------------------------------------------------

var browserDocuments = struct {
	mu   sync.Mutex
	docs map[string]*webDocument
}{docs: make(map[string]*webDocument)}

// setBrowserDocument stores the parsed page for a browser variable, replacing
// the previous one so only the pages actually open stay in memory.
func setBrowserDocument(variable string, doc *webDocument) {
	if variable == "" {
		return
	}
	browserDocuments.mu.Lock()
	defer browserDocuments.mu.Unlock()
	if doc == nil {
		delete(browserDocuments.docs, variable)
		return
	}
	if len(browserDocuments.docs) >= 2 {
		if _, exists := browserDocuments.docs[variable]; !exists {
			for key := range browserDocuments.docs {
				delete(browserDocuments.docs, key)
				break
			}
		}
	}
	browserDocuments.docs[variable] = doc
}

// browserDocumentFor returns the parsed page for a variable, or nil when the
// current content is plain text.
func browserDocumentFor(variable string) *webDocument {
	if variable == "" {
		return nil
	}
	browserDocuments.mu.Lock()
	defer browserDocuments.mu.Unlock()
	return browserDocuments.docs[variable]
}

func resetBrowserDocumentsForTest() {
	browserDocuments.mu.Lock()
	browserDocuments.docs = make(map[string]*webDocument)
	browserDocuments.mu.Unlock()
}

// --- CSS parsing ----------------------------------------------------------

// webMediaBlockApplies decides whether the rules inside an at-rule block belong
// on the reader's screen.
//
// The reader has exactly one medium: a handheld screen. So a width, orientation
// or feature query keeps its rules — the reader is the media that matters — but
// a block that only targets `print` or `speech` must not. A print sheet strips
// the page's navigation with display:none and re-colours it for paper; applying
// that on the screen is how a page arrives with its header and menu missing.
func webMediaBlockApplies(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	query = strings.TrimSpace(strings.TrimPrefix(query, "@media"))
	for _, part := range strings.Split(query, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		negated := strings.HasPrefix(part, "not ")
		applies := true
		for _, field := range strings.Fields(part) {
			switch field {
			case "and", "not", "only", "or":
				continue
			}
			if strings.HasPrefix(field, "(") {
				// A feature query with no media type is the screen's.
				break
			}
			switch strings.Trim(field, ",") {
			case "print", "speech":
				applies = false
			}
			break
		}
		if negated {
			applies = !applies
		}
		if applies {
			return true
		}
	}
	return false
}

// parseWebCSS turns a stylesheet into rules. An at-rule block is skipped unless
// it describes this screen: @media (except print/speech), @supports (a modern
// reader supports what it implements) and @layer (the cascade order is
// approximated, but the rules are the site's real styling).
func parseWebCSS(src string) []webRule {
	src = stripWebCSSComments(src)
	rules := make([]webRule, 0, 32)
	order := 0
	for i := 0; i < len(src); {
		open := strings.IndexByte(src[i:], '{')
		if open < 0 {
			break
		}
		open += i
		selectorText := strings.TrimSpace(src[i:open])
		close := webCSSBlockEnd(src, open)
		if close < 0 {
			break
		}
		body := src[open+1 : close]
		i = close + 1
		if selectorText == "" {
			continue
		}
		if strings.HasPrefix(selectorText, "@") {
			lower := strings.ToLower(selectorText)
			keep := strings.HasPrefix(lower, "@supports") || strings.HasPrefix(lower, "@layer")
			if strings.HasPrefix(lower, "@media") {
				keep = webMediaBlockApplies(selectorText)
			}
			// A conditional group rule nests: `@media` inside `@layer` still
			// has to be judged on its own query.
			if keep {
				for _, inner := range parseWebCSS(body) {
					inner.order = order
					order++
					rules = append(rules, inner)
				}
			}
			continue
		}
		decls, important := parseWebDeclsSplit(body)
		if len(decls) == 0 && len(important) == 0 {
			continue
		}
		for _, selector := range strings.Split(selectorText, ",") {
			selector = strings.TrimSpace(selector)
			if selector == "" {
				continue
			}
			rules = append(rules, webRule{
				selector:    selector,
				specificity: webSelectorSpecificity(selector),
				order:       order,
				decls:       decls,
				important:   important,
			})
			order++
		}
	}
	return rules
}

func stripWebCSSComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	for i := 0; i < len(src); {
		if src[i] == '/' && i+1 < len(src) && src[i+1] == '*' {
			if end := strings.Index(src[i+2:], "*/"); end >= 0 {
				i += 2 + end + 2
			} else {
				break
			}
			continue
		}
		out.WriteByte(src[i])
		i++
	}
	return out.String()
}

// webCSSBlockEnd finds the closing brace of a block, skipping braces inside
// strings and comments.
func webCSSBlockEnd(src string, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// parseWebDeclsSplit reads one declaration block. Declarations marked
// !important come back separately so the cascade can apply them last.
func parseWebDeclsSplit(src string) (decls, important map[string]string) {
	decls = make(map[string]string, 8)
	var quote byte
	start := 0
	src += ";"
	for i := 0; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case ';':
			piece := src[start:i]
			start = i + 1
			colon := strings.IndexByte(piece, ':')
			if colon <= 0 {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(piece[:colon]))
			value := strings.TrimSpace(piece[colon+1:])
			if name == "" || value == "" {
				continue
			}
			if stripped, marked := webDropImportant(value); marked {
				if stripped == "" {
					continue
				}
				if important == nil {
					important = make(map[string]string, 2)
				}
				important[name] = stripped
				continue
			}
			decls[name] = value
		}
	}
	return decls, important
}

// webDropImportant removes a trailing !important, in any letter case, and
// reports whether it was there. The flag has to be stripped before the value
// reaches a length or colour parser.
func webDropImportant(value string) (string, bool) {
	lower := strings.ToLower(value)
	idx := strings.LastIndex(lower, "!important")
	if idx < 0 {
		return value, false
	}
	if strings.TrimSpace(value[idx+len("!important"):]) != "" {
		return value, false
	}
	return strings.TrimSpace(value[:idx]), true
}

// --- Custom properties, var() and calc() ----------------------------------

// webVarScopeFor builds the scope one element sees. Inherited properties are
// already expanded, so only the element's own ones are resolved here.
func webVarScopeFor(parent *webVarScope, decls map[string]string) *webVarScope {
	var own map[string]string
	for name, value := range decls {
		if !strings.HasPrefix(name, "--") {
			continue
		}
		if own == nil {
			own = make(map[string]string, 4)
		}
		own[name] = value
	}
	if len(own) == 0 {
		return parent
	}
	props := make(map[string]string, len(own)+4)
	if parent != nil {
		for name, value := range parent.props {
			props[name] = value
		}
	}
	visiting := make(map[string]bool, len(own))
	for name := range own {
		if value, ok := webResolveCustomProperty(name, own, props, visiting, 0); ok {
			props[name] = value
		}
	}
	return &webVarScope{props: props}
}

// webResolveCustomProperty expands one `--name` to text with no var() left in
// it. A cycle (`--a: var(--b); --b: var(--a)`) is invalid at computed-value
// time, so it reports false and the property leaves the scope, which is what
// makes a later `var(--a, 12px)` fall back to 12px.
func webResolveCustomProperty(name string, own, props map[string]string, visiting map[string]bool, depth int) (string, bool) {
	if depth > 8 || visiting[name] {
		return "", false
	}
	raw, ok := own[name]
	if !ok {
		raw, ok = props[name]
	}
	if !ok {
		return "", false
	}
	if !strings.Contains(strings.ToLower(raw), "var(") {
		return raw, true
	}
	visiting[name] = true
	defer delete(visiting, name)
	value, changed := webReplaceVarRefs(raw, own, props, visiting, depth)
	if !changed {
		return "", false
	}
	return value, true
}

// webSubstituteVars replaces every var() in an ordinary declaration value.
// A reference that resolves to nothing and has no fallback is invalid at
// computed-value time: the whole declaration is then left as authored and the
// property parsers ignore it, which is how a browser drops it too.
func webSubstituteVars(value string, scope *webVarScope) string {
	if !strings.Contains(strings.ToLower(value), "var(") {
		return value
	}
	var props map[string]string
	if scope != nil {
		props = scope.props
	}
	current := value
	for pass := 0; pass < 8; pass++ {
		if !strings.Contains(strings.ToLower(current), "var(") {
			return current
		}
		next, changed := webReplaceVarRefs(current, nil, props, map[string]bool{}, 0)
		if !changed {
			return value
		}
		current = next
	}
	return value
}

// webReplaceVarRefs substitutes the var() calls in one value. It reports false
// when a reference cannot be resolved at all, so the caller can drop the
// declaration instead of leaving a half-substituted string behind.
func webReplaceVarRefs(value string, own, props map[string]string, visiting map[string]bool, depth int) (string, bool) {
	var out strings.Builder
	out.Grow(len(value))
	lower := strings.ToLower(value)
	changed := false
	for i := 0; i < len(value); {
		idx := strings.Index(lower[i:], "var(")
		if idx < 0 {
			out.WriteString(value[i:])
			break
		}
		idx += i
		out.WriteString(value[i:idx])
		end := webCSSParenEnd(value, idx+3)
		if end < 0 {
			return "", false
		}
		name, fallback, hasFallback := webVarArgs(value[idx+4 : end])
		replacement, ok := webResolveCustomProperty(name, own, props, visiting, depth+1)
		if !ok {
			if !hasFallback {
				return "", false
			}
			// A fallback may itself hold var() calls, but plain text is the
			// common case and has nothing to expand.
			if nested, nestedChanged := webReplaceVarRefs(fallback, own, props, visiting, depth+1); nestedChanged {
				replacement = nested
			} else {
				replacement = fallback
			}
		}
		out.WriteString(replacement)
		changed = true
		i = end + 1
	}
	if !changed {
		return value, false
	}
	return out.String(), true
}

// webVarArgs splits the inside of var(...) into the property name and its
// fallback. Only a comma outside brackets separates the two.
func webVarArgs(inner string) (name, fallback string, hasFallback bool) {
	depth := 0
	var quote byte
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(inner[:i]), strings.TrimSpace(inner[i+1:]), true
			}
		}
	}
	return strings.TrimSpace(inner), "", false
}

// webCSSParenEnd finds the bracket matching the one at open, skipping brackets
// inside quoted strings.
func webCSSParenEnd(src string, open int) int {
	depth := 0
	var quote byte
	for i := open; i < len(src); i++ {
		c := src[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// webResolveValue prepares one declaration for the property parsers: var()
// substituted first, then any calc() that can be reduced to a px length.
func webResolveValue(value string, scope *webVarScope, em float64) string {
	value = webSubstituteVars(value, scope)
	if strings.Contains(strings.ToLower(value), "calc(") {
		value = webSubstituteCalc(value, em)
	}
	return value
}

// webSubstituteCalc replaces each calc() it can evaluate with a px length. A
// calc() that mixes in a unit the reader cannot resolve yet (`100% - 20px`)
// is left as authored.
func webSubstituteCalc(value string, em float64) string {
	if em <= 0 {
		em = 16
	}
	lower := strings.ToLower(value)
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); {
		idx := strings.Index(lower[i:], "calc(")
		if idx < 0 {
			out.WriteString(value[i:])
			break
		}
		idx += i
		end := webCSSParenEnd(value, idx+4)
		if end < 0 {
			out.WriteString(value[i:])
			break
		}
		out.WriteString(value[i:idx])
		if px, ok := webEvalCalc(value[idx+5:end], em); ok {
			out.WriteString(strconv.FormatFloat(px, 'f', -1, 64))
			out.WriteString("px")
		} else {
			out.WriteString(value[idx : end+1])
		}
		i = end + 1
	}
	return out.String()
}

// webCalcValue is one operand of a calc() expression. A unitless number is
// only legal against another unitless number or as a multiplier.
type webCalcValue struct {
	px       float64
	unitless bool
}

// webCalcParser evaluates the arithmetic inside calc(). It handles + - * /,
// nested brackets and px/em/rem operands; anything else fails and the caller
// keeps the original text.
type webCalcParser struct {
	src string
	pos int
	em  float64
}

func webEvalCalc(expr string, em float64) (float64, bool) {
	p := &webCalcParser{src: expr, em: em}
	value, ok := p.sum()
	if !ok {
		return 0, false
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return 0, false
	}
	return value.px, true
}

func (p *webCalcParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\n') {
		p.pos++
	}
}

func (p *webCalcParser) sum() (webCalcValue, bool) {
	left, ok := p.product()
	if !ok {
		return webCalcValue{}, false
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || (p.src[p.pos] != '+' && p.src[p.pos] != '-') {
			return left, true
		}
		op := p.src[p.pos]
		p.pos++
		right, ok := p.product()
		if !ok || left.unitless != right.unitless {
			return webCalcValue{}, false
		}
		if op == '+' {
			left.px += right.px
		} else {
			left.px -= right.px
		}
	}
}

func (p *webCalcParser) product() (webCalcValue, bool) {
	left, ok := p.factor()
	if !ok {
		return webCalcValue{}, false
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) || (p.src[p.pos] != '*' && p.src[p.pos] != '/') {
			return left, true
		}
		op := p.src[p.pos]
		p.pos++
		right, ok := p.factor()
		if !ok {
			return webCalcValue{}, false
		}
		if op == '*' {
			if !left.unitless && !right.unitless {
				return webCalcValue{}, false
			}
			left.px *= right.px
			left.unitless = left.unitless && right.unitless
			continue
		}
		if !right.unitless || right.px == 0 {
			return webCalcValue{}, false
		}
		left.px /= right.px
	}
}

func (p *webCalcParser) factor() (webCalcValue, bool) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return webCalcValue{}, false
	}
	switch p.src[p.pos] {
	case '(':
		p.pos++
		value, ok := p.sum()
		if !ok {
			return webCalcValue{}, false
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return webCalcValue{}, false
		}
		p.pos++
		return value, true
	case '-', '+':
		sign := 1.0
		if p.src[p.pos] == '-' {
			sign = -1
		}
		p.pos++
		value, ok := p.factor()
		if !ok {
			return webCalcValue{}, false
		}
		value.px *= sign
		return value, true
	}
	start := p.pos
	for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
		p.pos++
	}
	if p.pos == start {
		return webCalcValue{}, false
	}
	number, err := strconv.ParseFloat(p.src[start:p.pos], 64)
	if err != nil {
		return webCalcValue{}, false
	}
	unitStart := p.pos
	for p.pos < len(p.src) && (p.src[p.pos] >= 'a' && p.src[p.pos] <= 'z' || p.src[p.pos] == '%') {
		p.pos++
	}
	switch strings.ToLower(p.src[unitStart:p.pos]) {
	case "":
		return webCalcValue{px: number, unitless: true}, true
	case "px":
		return webCalcValue{px: number}, true
	case "em":
		return webCalcValue{px: number * p.em}, true
	case "rem":
		return webCalcValue{px: number * 16}, true
	}
	return webCalcValue{}, false
}

// webSelectorParts splits a selector into compounds on descendant combinators.
// Child/sibling combinators are treated as descendant, which is close enough
// for a reader and never matches less than the real thing would.
func webSelectorParts(sel string) []string {
	var parts []string
	start := -1
	for i := 0; i < len(sel); i++ {
		switch sel[i] {
		case ' ', '\t', '\n', '>', '+', '~':
			if start >= 0 {
				parts = append(parts, sel[start:i])
				start = -1
			}
		default:
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		parts = append(parts, sel[start:])
	}
	return parts
}

// webCompoundTokens splits `div.note#main:hover` into its simple selectors.
func webCompoundTokens(compound string) []string {
	var tokens []string
	for i := 0; i < len(compound); {
		switch compound[i] {
		case '.', '#':
			start := i
			i++
			for i < len(compound) && webSelectorNameByte(compound[i]) {
				i++
			}
			tokens = append(tokens, compound[start:i])
		case '[':
			start := i
			if end := strings.IndexByte(compound[i:], ']'); end >= 0 {
				i += end + 1
			} else {
				i = len(compound)
			}
			tokens = append(tokens, compound[start:i])
		case ':':
			start := i
			i++
			for i < len(compound) && compound[i] == ':' {
				i++
			}
			for i < len(compound) && webSelectorNameByte(compound[i]) {
				i++
			}
			if i < len(compound) && compound[i] == '(' {
				if end := strings.IndexByte(compound[i:], ')'); end >= 0 {
					i += end + 1
				}
			}
			tokens = append(tokens, compound[start:i])
		default:
			start := i
			for i < len(compound) && compound[i] != '.' && compound[i] != '#' &&
				compound[i] != ':' && compound[i] != '[' {
				i++
			}
			if token := strings.TrimSpace(compound[start:i]); token != "" {
				tokens = append(tokens, token)
			}
		}
	}
	return tokens
}

func webSelectorNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '_'
}

func webSelectorSpecificity(selector string) int {
	ids, classes, tags := 0, 0, 0
	for _, part := range webSelectorParts(selector) {
		for _, token := range webCompoundTokens(part) {
			switch {
			case strings.HasPrefix(token, "#"):
				ids++
			case strings.HasPrefix(token, "."):
				classes++
			case strings.HasPrefix(token, "[") || strings.HasPrefix(token, ":"), token == "*":
				// Attribute and pseudo selectors cannot be evaluated against
				// this document model; they contribute only specificity.
			default:
				tags++
			}
		}
	}
	return ids*100 + classes*10 + tags
}

// webSelectorMatches matches a selector against the chain of open elements,
// right to left, which is how descendant selectors are supposed to behave.
func webSelectorMatches(selector string, chain []webElement) bool {
	parts := webSelectorParts(selector)
	if len(parts) == 0 || len(chain) == 0 {
		return false
	}
	if !webCompoundMatches(parts[len(parts)-1], chain[len(chain)-1]) {
		return false
	}
	idx := len(chain) - 2
	for pi := len(parts) - 2; pi >= 0; pi-- {
		matched := false
		for ; idx >= 0; idx-- {
			if webCompoundMatches(parts[pi], chain[idx]) {
				matched = true
				idx--
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func webCompoundMatches(compound string, el webElement) bool {
	for _, token := range webCompoundTokens(compound) {
		switch {
		case token == "" || token == "*":
		case strings.HasPrefix(token, "#"):
			if !strings.EqualFold(el.id, token[1:]) {
				return false
			}
		case strings.HasPrefix(token, "."):
			cls := token[1:]
			found := false
			for _, have := range el.classes {
				if strings.EqualFold(have, cls) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case strings.HasPrefix(token, "[") || strings.HasPrefix(token, ":"):
			// Cannot evaluate attrs/pseudos; assume they hold rather than
			// dropping an otherwise valid rule.
		default:
			if !strings.EqualFold(token, el.tag) {
				return false
			}
		}
	}
	return true
}

// --- CSS values -----------------------------------------------------------

var webNamedColors = map[string]sdl.Color{
	"black":       {R: 0, G: 0, B: 0, A: 255},
	"white":       {R: 255, G: 255, B: 255, A: 255},
	"red":         {R: 220, G: 50, B: 47, A: 255},
	"green":       {R: 40, G: 140, B: 70, A: 255},
	"blue":        {R: 30, G: 90, B: 200, A: 255},
	"yellow":      {R: 240, G: 200, B: 0, A: 255},
	"orange":      {R: 235, G: 130, B: 30, A: 255},
	"purple":      {R: 120, G: 60, B: 160, A: 255},
	"gray":        {R: 128, G: 128, B: 128, A: 255},
	"grey":        {R: 128, G: 128, B: 128, A: 255},
	"silver":      {R: 192, G: 192, B: 192, A: 255},
	"maroon":      {R: 128, G: 0, B: 0, A: 255},
	"olive":       {R: 128, G: 128, B: 0, A: 255},
	"lime":        {R: 0, G: 200, B: 0, A: 255},
	"aqua":        {R: 0, G: 180, B: 180, A: 255},
	"cyan":        {R: 0, G: 180, B: 180, A: 255},
	"teal":        {R: 0, G: 128, B: 128, A: 255},
	"navy":        {R: 0, G: 0, B: 128, A: 255},
	"fuchsia":     {R: 200, G: 40, B: 190, A: 255},
	"magenta":     {R: 200, G: 40, B: 190, A: 255},
	"brown":       {R: 140, G: 80, B: 45, A: 255},
	"pink":        {R: 235, G: 140, B: 175, A: 255},
	"gold":        {R: 215, G: 175, B: 45, A: 255},
	"beige":       {R: 245, G: 245, B: 220, A: 255},
	"ivory":       {R: 255, G: 255, B: 240, A: 255},
	"khaki":       {R: 240, G: 230, B: 140, A: 255},
	"coral":       {R: 255, G: 127, B: 80, A: 255},
	"crimson":     {R: 220, G: 20, B: 60, A: 255},
	"indigo":      {R: 75, G: 0, B: 130, A: 255},
	"violet":      {R: 200, G: 130, B: 220, A: 255},
	"turquoise":   {R: 64, G: 200, B: 190, A: 255},
	"salmon":      {R: 250, G: 128, B: 114, A: 255},
	"tomato":      {R: 255, G: 99, B: 71, A: 255},
	"steelblue":   {R: 70, G: 130, B: 180, A: 255},
	"darkgray":    {R: 90, G: 90, B: 90, A: 255},
	"darkgrey":    {R: 90, G: 90, B: 90, A: 255},
	"lightgray":   {R: 211, G: 211, B: 211, A: 255},
	"lightgrey":   {R: 211, G: 211, B: 211, A: 255},
	"whitesmoke":  {R: 245, G: 245, B: 245, A: 255},
	"gainsboro":   {R: 220, G: 220, B: 220, A: 255},
	"dimgray":     {R: 105, G: 105, B: 105, A: 255},
	"transparent": {R: 0, G: 0, B: 0, A: 0},
}

// parseWebColor handles hex, rgb()/rgba() and the common named colours. Alpha
// is ignored: the page background decides what is readable here.
func parseWebColor(value string) (sdl.Color, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return sdl.Color{}, false
	}
	if strings.HasPrefix(v, "#") {
		hex := v[1:]
		switch len(hex) {
		case 3, 4:
			r, ok1 := webHexByte(string([]byte{hex[0], hex[0]}))
			g, ok2 := webHexByte(string([]byte{hex[1], hex[1]}))
			b, ok3 := webHexByte(string([]byte{hex[2], hex[2]}))
			if ok1 && ok2 && ok3 {
				return sdl.Color{R: r, G: g, B: b, A: 255}, true
			}
		case 6, 8:
			r, ok1 := webHexByte(hex[0:2])
			g, ok2 := webHexByte(hex[2:4])
			b, ok3 := webHexByte(hex[4:6])
			if ok1 && ok2 && ok3 {
				return sdl.Color{R: r, G: g, B: b, A: 255}, true
			}
		}
		return sdl.Color{}, false
	}
	if strings.HasPrefix(v, "rgb") {
		open := strings.IndexByte(v, '(')
		close := strings.LastIndexByte(v, ')')
		if open < 0 || close <= open {
			return sdl.Color{}, false
		}
		parts := strings.FieldsFunc(v[open+1:close], func(r rune) bool { return r == ',' || r == ' ' || r == '/' })
		if len(parts) < 3 {
			return sdl.Color{}, false
		}
		vals := make([]uint8, 3)
		for i := 0; i < 3; i++ {
			part := strings.TrimSpace(parts[i])
			percent := strings.HasSuffix(part, "%")
			part = strings.TrimSuffix(part, "%")
			n, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil {
				return sdl.Color{}, false
			}
			if percent {
				n = n * 255 / 100
			}
			n = math.Round(n)
			if n < 0 {
				n = 0
			}
			if n > 255 {
				n = 255
			}
			vals[i] = uint8(n)
		}
		return sdl.Color{R: vals[0], G: vals[1], B: vals[2], A: 255}, true
	}
	if c, ok := webNamedColors[v]; ok {
		if c.A == 0 {
			return sdl.Color{}, false
		}
		return c, true
	}
	return sdl.Color{}, false
}

func webHexByte(text string) (uint8, bool) {
	n, err := strconv.ParseUint(text, 16, 8)
	if err != nil {
		return 0, false
	}
	return uint8(n), true
}

// webLength converts a CSS length to px. Percentages resolve against rel.
func webLength(value string, rel float64) float64 {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "auto" || v == "none" {
		return 0
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(v, "rem"):
		v = strings.TrimSuffix(v, "rem")
		mult = 16
	case strings.HasSuffix(v, "em"):
		v = strings.TrimSuffix(v, "em")
		mult = rel
		if mult <= 0 {
			mult = 16
		}
	case strings.HasSuffix(v, "px"):
		v = strings.TrimSuffix(v, "px")
	case strings.HasSuffix(v, "pt"):
		v = strings.TrimSuffix(v, "pt")
		mult = 1.333
	case strings.HasSuffix(v, "%"):
		v = strings.TrimSuffix(v, "%")
		mult = rel / 100
	case strings.HasSuffix(v, "ex"), strings.HasSuffix(v, "ch"):
		v = strings.TrimRightFunc(v, func(r rune) bool { return r >= 'a' && r <= 'z' })
		mult = rel / 2
	case strings.HasSuffix(v, "vw"), strings.HasSuffix(v, "vh"),
		strings.HasSuffix(v, "vmin"), strings.HasSuffix(v, "vmax"):
		// Viewport units describe the page's own window; the reader has no
		// such window, so they must not turn into indentation.
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return n * mult
}

// webFontSizePx resolves font-size against the inherited size.
func webFontSizePx(value string, parent float64) (float64, bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "xx-small":
		return 9, true
	case "x-small":
		return 11, true
	case "small":
		return 13, true
	case "medium":
		return 16, true
	case "large":
		return 18, true
	case "x-large":
		return 24, true
	case "xx-large":
		return 32, true
	case "smaller":
		return parent * 0.85, true
	case "larger":
		return parent * 1.2, true
	}
	px := webLength(v, parent)
	if px <= 0 {
		return 0, false
	}
	return px, true
}

// webMarginPair reads the vertical values of a margin shorthand.
func webMarginPair(value string, fontPx float64) (top, bottom float64) {
	top, _, bottom, _, valid := webBoxShorthand(value, fontPx)
	if !valid {
		return 0, 0
	}
	return top, bottom
}

// webBoxShorthand expands a one-to-four value CSS box shorthand into its top,
// right, bottom and left lengths.
func webBoxShorthand(value string, fontPx float64) (top, right, bottom, left float64, ok bool) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, 0, 0, 0, false
	}
	top = webLength(fields[0], fontPx)
	switch len(fields) {
	case 1:
		right, bottom, left = top, top, top
	case 2:
		right = webLength(fields[1], fontPx)
		bottom, left = top, right
	case 3:
		right = webLength(fields[1], fontPx)
		bottom, left = webLength(fields[2], fontPx), right
	default:
		right = webLength(fields[1], fontPx)
		bottom = webLength(fields[2], fontPx)
		left = webLength(fields[3], fontPx)
	}
	return top, right, bottom, left, true
}

func webTextAlign(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "center":
		return 1
	case "right", "end":
		return 2
	}
	return 0
}

// webTextTransform decodes text-transform into the layout's small enum.
func webTextTransform(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "uppercase":
		return 1
	case "lowercase":
		return 2
	case "capitalize":
		return 3
	}
	return 0
}

// webTransformWord applies text-transform. It runs before measurement so the
// wrapped line matches the glyphs that are drawn (uppercase is wider).
func webTransformWord(word string, mode int) string {
	switch mode {
	case 1:
		return strings.ToUpper(word)
	case 2:
		return strings.ToLower(word)
	case 3:
		runes := []rune(word)
		if len(runes) == 0 {
			return word
		}
		return strings.ToUpper(string(runes[0])) + string(runes[1:])
	}
	return word
}

// webFontShorthand reads the font shorthand's `[style] [variant] [weight]
// [stretch] size[/line-height] family` form. Only what the reader draws is
// applied: the family is ignored because the app ships one face, and a system
// font keyword (caption, menu, ...) carries no explicit size to read.
func webFontShorthand(st *webStyle, value string) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "inherit" || v == "initial" || v == "unset" {
		return
	}
	switch v {
	case "caption", "icon", "menu", "message-box", "small-caption", "status-bar":
		return
	}
	fields := strings.Fields(v)
	size := 0.0
	lineHeight := 0.0
	found := false
	for _, field := range fields {
		if found {
			break
		}
		sizeToken, lineToken := field, ""
		if i := strings.IndexByte(field, '/'); i >= 0 {
			sizeToken, lineToken = field[:i], field[i+1:]
		}
		if !webIsFontSizeToken(sizeToken) {
			continue
		}
		px, ok := webFontSizePx(sizeToken, st.FontPx)
		if !ok {
			continue
		}
		found = true
		size = px
		if lineToken != "" {
			if lh, ok := webLineHeightPx(lineToken, px); ok {
				lineHeight = lh
			}
		}
	}
	if !found {
		// No size means this is not a font shorthand the reader understands;
		// guessing one would resize the whole page from a stray keyword.
		return
	}
	if size < 8 {
		size = 8
	}
	if size > 64 {
		size = 64
	}
	st.FontPx = size
	if lineHeight > 0 {
		st.LineHeight = lineHeight
	}
	for _, field := range fields {
		switch {
		case field == "italic", field == "oblique":
			st.Italic = true
		case field == "bold", field == "bolder":
			st.Bold = true
		case field == "lighter", field == "normal":
			// `normal` is the shorthand's reset for style and weight alike.
			if field == "lighter" {
				st.Bold = false
			}
		case len(field) == 3 && webAllDigits(field):
			if n, err := strconv.Atoi(field); err == nil {
				st.Bold = n >= 600
			}
		}
	}
}

// webIsFontSizeToken reports whether a token can be the size in a font
// shorthand. A bare number is a weight (`font: 700 16px Arial`), so it must not
// be read as a size.
func webIsFontSizeToken(token string) bool {
	t := strings.ToLower(strings.TrimSpace(token))
	if t == "" {
		return false
	}
	switch t {
	case "xx-small", "x-small", "small", "medium", "large", "x-large", "xx-large",
		"smaller", "larger", "small-caps":
		return true
	}
	for _, unit := range []string{"px", "em", "rem", "pt", "%", "ex", "ch", "vw", "vh"} {
		if strings.HasSuffix(t, unit) {
			return true
		}
	}
	return false
}

// webAllDigits reports whether every byte is an ASCII digit.
func webAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// webBorderWidthInValue reads the width token of a border shorthand: the first
// length in `1px solid #f00`, `solid 3px`, or a keyword. It returns 0 when the
// shorthand names no width at all (the caller keeps the reader's default).
func webBorderWidthInValue(value string, fontPx float64) float64 {
	for _, token := range strings.Fields(strings.ToLower(value)) {
		switch token {
		case "thin":
			return 1
		case "medium":
			return 3
		case "thick":
			return 5
		}
		// A colour and a border style are not lengths; webLength returns 0 for
		// both, so only a real measurement can win here.
		if px := webLength(token, fontPx); px > 0 {
			return px
		}
	}
	return 0
}

// webMaxAbs keeps the larger magnitude, so a later `border-left: 1px` does not
// shrink a card whose other edges were drawn 3px.
func webMaxAbs(a, b float64) float64 {
	if b > a {
		return b
	}
	return a
}

// webBorderVisible reports whether a border shorthand actually paints. Many
// sheets reset borders with `border: none` or `border-top: 0`, which must not
// grow a rule of its own.
func webBorderVisible(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" || v == "0" || v == "none" || v == "hidden" {
		return false
	}
	if strings.Contains(v, "none") || strings.Contains(v, "hidden") {
		return false
	}
	return true
}

// webColorInValue finds the colour in a shorthand such as
// `background: #123 url(x.png) no-repeat` or `1px solid rgba(0,0,0,.2)`.
func webColorInValue(value string) (sdl.Color, bool) {
	if c, ok := parseWebColor(value); ok {
		return c, true
	}
	lower := strings.ToLower(value)
	if idx := strings.Index(lower, "rgb"); idx >= 0 {
		if open := strings.IndexByte(lower[idx:], '('); open >= 0 {
			if end := strings.IndexByte(lower[idx+open:], ')'); end >= 0 {
				if c, ok := parseWebColor(lower[idx : idx+open+end+1]); ok {
					return c, true
				}
			}
		}
	}
	for _, field := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '/'
	}) {
		if c, ok := parseWebColor(field); ok {
			return c, true
		}
	}
	return sdl.Color{}, false
}

// --- Style resolution -----------------------------------------------------

func webBaseStyle() webStyle {
	return webStyle{FontPx: 16}
}

// applyWebTagDefaults gives the reader its browser-ish baseline: headings are
// large and bold, paragraphs are spaced, lists are indented, quotes carry a
// rule and code is monospaced-ish on a tinted panel.
func applyWebTagDefaults(st *webStyle, tag string) {
	switch tag {
	case "h1":
		st.FontPx, st.Bold = 30, true
		st.MarginTop, st.MarginBot = 20, 10
	case "h2":
		st.FontPx, st.Bold = 25, true
		st.MarginTop, st.MarginBot = 18, 8
	case "h3":
		st.FontPx, st.Bold = 21, true
		st.MarginTop, st.MarginBot = 16, 6
	case "h4", "h5", "h6":
		st.FontPx, st.Bold = 18, true
		st.MarginTop, st.MarginBot = 12, 4
	case "p":
		// Browsers give a paragraph one em above and below; without it the
		// rows of two paragraphs merge into one undifferentiated wall.
		st.MarginTop, st.MarginBot = 16, 16
	case "blockquote":
		st.Indent += 16
		st.MarginTop, st.MarginBot = 16, 16
		st.BorderLeft = true
	case "pre":
		st.Pre = true
		st.FontPx = 13
		st.MarginTop, st.MarginBot = 16, 16
		st.HasBg = true
		st.Background = sdl.Color{R: 238, G: 241, B: 246, A: 255}
	case "code", "kbd", "samp":
		st.FontPx = 13
		st.HasBg = true
		st.Background = sdl.Color{R: 238, G: 241, B: 246, A: 255}
	case "li":
		st.Indent += 18
		st.MarginTop, st.MarginBot = 2, 2
	case "a":
		st.Underline = true
		st.Link = true
	case "strong", "b":
		st.Bold = true
	case "em", "i":
		st.Italic = true
	case "s", "del", "strike":
		st.Strike = true
	case "u", "ins":
		st.Underline = true
	case "center":
		st.Align = 1
	case "dt":
		// Definition terms are bold in every browser.
		st.Bold = true
	case "mark":
		st.HasBg = true
		st.Background = sdl.Color{R: 255, G: 233, B: 120, A: 255}
	case "small":
		if st.FontPx > 12 {
			st.FontPx -= 2
		}
	case "td", "th":
		st.Indent += 4
		if tag == "th" {
			st.Bold = true
		}
	}
}

func applyWebDecls(st *webStyle, decls map[string]string) {
	if len(decls) == 0 {
		return
	}
	// The `font` shorthand comes before the longhands, so a sheet that sets
	// both ends up with the longhand, the way a later declaration wins in a
	// browser. It carries the size (and therefore every em length below), the
	// weight, the style and the line-height; a reset written as
	// `font: 16px/1.6 system-ui` used to leave the whole page at the reader's
	// default size.
	if v, ok := decls["font"]; ok {
		webFontShorthand(st, v)
	}
	// font-size next: em-based lengths below resolve against it.
	if v, ok := decls["font-size"]; ok {
		if px, valid := webFontSizePx(v, st.FontPx); valid {
			if px < 8 {
				px = 8
			}
			if px > 64 {
				px = 64
			}
			st.FontPx = px
		}
	}
	if v, ok := decls["color"]; ok {
		if c, valid := parseWebColor(v); valid {
			st.Color, st.HasColor = c, true
		}
	}
	for _, key := range []string{"background-color", "background"} {
		if v, ok := decls[key]; ok {
			if c, valid := webColorInValue(v); valid {
				st.Background, st.HasBg = c, true
				break
			}
		}
	}
	// A linear gradient becomes a ramp the reader paints band by band, so the
	// header keeps its light and dark ends instead of one flat colour. The
	// average is still stored as the background: contrast and fade decisions
	// need a single colour, and a radial or conic gradient — not a ramp — stays
	// a flat averaged fill. An explicit background-color still wins.
	if !st.HasBg {
		for _, key := range []string{"background-image", "background"} {
			v, ok := decls[key]
			if !ok {
				continue
			}
			if g, valid := webParseGradient(v); valid {
				ramp := g
				st.Gradient, st.HasGradient = &ramp, true
				st.Background, st.HasBg = webGradientAverage(g), true
				break
			}
			if c, valid := webGradientColor(v); valid {
				st.Background, st.HasBg = c, true
				break
			}
		}
	}
	if v, ok := decls["font-weight"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "bold", "bolder", "600", "700", "800", "900":
			st.Bold = true
		case "normal", "lighter", "100", "200", "300", "400", "500":
			st.Bold = false
		}
	}
	if v, ok := decls["font-style"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		st.Italic = strings.HasPrefix(lower, "italic") || strings.HasPrefix(lower, "oblique")
	}
	for _, key := range []string{"text-decoration", "text-decoration-line"} {
		if v, ok := decls[key]; ok {
			lower := strings.ToLower(v)
			st.Underline = strings.Contains(lower, "underline")
			st.Strike = strings.Contains(lower, "line-through")
			break
		}
	}
	if v, ok := decls["text-transform"]; ok {
		st.Transform = webTextTransform(v)
	}
	if v, ok := decls["text-align"]; ok {
		st.Align = webTextAlign(v)
	}
	if v, ok := decls["text-indent"]; ok {
		st.TextIndent = webLength(v, st.FontPx)
	}
	if v, ok := decls["white-space"]; ok {
		st.Pre = strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "pre")
	}
	for _, key := range []string{"display", "visibility"} {
		if v, ok := decls[key]; ok {
			lower := strings.ToLower(strings.TrimSpace(v))
			if lower == "none" || lower == "hidden" {
				st.Hidden = true
			}
		}
	}
	if v, ok := decls["margin"]; ok {
		if top, right, bottom, left, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop, st.MarginBot = top, bottom
			st.Indent += left
			st.IndentRight += right
		} else {
			top, bottom := webMarginPair(v, st.FontPx)
			st.MarginTop, st.MarginBot = top, bottom
		}
	}
	if v, ok := decls["margin-top"]; ok {
		st.MarginTop = webLength(v, st.FontPx)
	}
	if v, ok := decls["margin-bottom"]; ok {
		st.MarginBot = webLength(v, st.FontPx)
	}
	if v, ok := decls["padding-left"]; ok {
		st.Indent += webLength(v, st.FontPx)
	}
	// A block's own top and bottom padding is breathing room, exactly like a
	// margin: without it a card written as `padding-top: 24px` sat flush
	// against the paragraph above it.
	if v, ok := decls["padding-top"]; ok {
		st.MarginTop += webLength(v, st.FontPx)
	}
	if v, ok := decls["padding-bottom"]; ok {
		st.MarginBot += webLength(v, st.FontPx)
	}
	// The logical shorthands every modern sheet uses. The reader is a
	// left-to-right column, so inline = left/right and block = top/bottom;
	// supporting them keeps a spacing writer's intent instead of dropping it.
	if v, ok := decls["margin-block"]; ok {
		if top, _, bottom, _, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop += top
			st.MarginBot += bottom
		}
	}
	if v, ok := decls["padding-block"]; ok {
		if top, _, bottom, _, valid := webBoxShorthand(v, st.FontPx); valid {
			st.MarginTop += top
			st.MarginBot += bottom
		}
	}
	for _, key := range []string{"margin-inline", "padding-inline"} {
		if v, ok := decls[key]; ok {
			if _, right, _, left, valid := webBoxShorthand(v, st.FontPx); valid {
				st.Indent += left
				st.IndentRight += right
			}
		}
	}
	for key, target := range map[string]*float64{
		"margin-inline-start":  &st.Indent,
		"padding-inline-start": &st.Indent,
		"margin-inline-end":    &st.IndentRight,
		"padding-inline-end":   &st.IndentRight,
		"margin-block-start":   &st.MarginTop,
		"padding-block-start":  &st.MarginTop,
		"margin-block-end":     &st.MarginBot,
		"padding-block-end":    &st.MarginBot,
	} {
		if v, ok := decls[key]; ok {
			*target += webLength(v, st.FontPx)
		}
	}
	if v, ok := decls["padding"]; ok {
		if top, right, bottom, left, valid := webBoxShorthand(v, st.FontPx); valid {
			st.Indent += left
			st.IndentRight += right
			st.MarginTop += top
			st.MarginBot += bottom
		}
	}
	if v, ok := decls["padding-right"]; ok {
		st.IndentRight += webLength(v, st.FontPx)
	}
	// margin-left/margin-right are the box the paragraph sits in: a 24px
	// gutter narrows the line on that side. `auto` reads as 0, which is the
	// honest result for a box the reader cannot measure.
	if v, ok := decls["margin-left"]; ok {
		st.Indent += webLength(v, st.FontPx)
	}
	if v, ok := decls["margin-right"]; ok {
		st.IndentRight += webLength(v, st.FontPx)
	}
	if v, ok := decls["line-height"]; ok {
		if px, valid := webLineHeightPx(v, st.FontPx); valid {
			st.LineHeight = px
		}
	}
	if v, ok := decls["border-radius"]; ok {
		st.Radius = webFirstLength(v, st.FontPx)
	}
	for _, key := range []string{"border-top-left-radius", "border-top-right-radius", "border-bottom-left-radius", "border-bottom-right-radius"} {
		if v, ok := decls[key]; ok {
			if px := webFirstLength(v, st.FontPx); px > st.Radius {
				st.Radius = px
			}
		}
	}
	if v, ok := decls["opacity"]; ok {
		if alpha, valid := webOpacityValue(v); valid {
			st.HasOpacity, st.Opacity = true, alpha
		}
	}
	if v, ok := decls["border-color"]; ok {
		if c, valid := webColorInValue(v); valid {
			st.BorderColor, st.HasBorderColor = c, true
		}
	}
	if !st.HasBorderColor && st.HasColor {
		// Browsers draw an uncoloured border in currentColor, which is what
		// `border-top: 1px solid` on a nav or card expects.
		st.BorderColor, st.HasBorderColor = st.Color, true
	}
	// A border shorthand carries its own colour (`border: 1px solid #f00`),
	// which is the only place most sheets set one.
	for _, key := range []string{"border", "border-left", "border-right", "border-top", "border-bottom"} {
		if v, ok := decls[key]; ok {
			if c, valid := webColorInValue(v); valid {
				st.BorderColor, st.HasBorderColor = c, true
			}
		}
	}
	// Each edge maps to its own side: `border-right` used to set the left
	// flag, so a sidebar divider drew on the wrong edge of the box.
	if v, ok := decls["border-left"]; ok && webBorderVisible(v) {
		st.BorderLeft = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-right"]; ok && webBorderVisible(v) {
		st.BorderRight = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-top"]; ok && webBorderVisible(v) {
		st.BorderTop = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-bottom"]; ok && webBorderVisible(v) {
		st.BorderBottom = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	for key, target := range map[string]*bool{
		"border-left-width":   &st.BorderLeft,
		"border-right-width":  &st.BorderRight,
		"border-top-width":    &st.BorderTop,
		"border-bottom-width": &st.BorderBottom,
	} {
		if v, ok := decls[key]; ok {
			if px := webLength(v, st.FontPx); px > 0 {
				*target = true
				st.BorderWidth = webMaxAbs(st.BorderWidth, px)
			}
		}
	}
	if v, ok := decls["border"]; ok && webBorderVisible(v) {
		st.BoxBorder = true
		st.BorderWidth = webMaxAbs(st.BorderWidth, webBorderWidthInValue(v, st.FontPx))
	}
	if v, ok := decls["border-width"]; ok {
		if px := webLength(v, st.FontPx); px > 0 {
			st.BoxBorder = true
			st.BorderWidth = webMaxAbs(st.BorderWidth, px)
		}
	}
	for _, key := range []string{"list-style-type", "list-style"} {
		if v, ok := decls[key]; ok {
			if marker := webListStyleValue(v); marker != webListStyleDefault {
				st.ListStyle = marker
				if marker == webListStyleNone {
					st.ListNone = true
				}
				break
			}
		}
	}
	applyWebLayoutDecls(st, decls)
}

// applyWebLayoutDecls reads the flex and stacking properties. They are kept
// separate from the text properties because the layout engine consumes them as
// a unit: a flex container's settings describe its children, not its text.
func applyWebLayoutDecls(st *webStyle, decls map[string]string) {
	if v, ok := decls["display"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "flex", "inline-flex":
			st.Display = webDisplayFlex
		case "block", "flow-root", "grid", "inline-block":
			st.Display = webDisplayBlock
		}
	}
	if v, ok := decls["flex-direction"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		st.FlexDir = webFlexRow
		if strings.HasPrefix(lower, "column") {
			st.FlexDir = webFlexColumn
		}
	}
	if v, ok := decls["flex-wrap"]; ok {
		lower := strings.ToLower(strings.TrimSpace(v))
		st.FlexWrap = strings.HasPrefix(lower, "wrap")
	}
	if v, ok := decls["justify-content"]; ok {
		st.Justify = webJustifyValue(v)
	}
	if v, ok := decls["align-items"]; ok {
		st.AlignItems = webAlignValue(v)
	}
	if v, ok := decls["align-self"]; ok {
		st.AlignSelf = webAlignValue(v)
	}
	if v, ok := decls["gap"]; ok {
		fields := strings.Fields(v)
		st.GapX = webLength(fields[0], st.FontPx)
		st.GapY = st.GapX
		if len(fields) > 1 {
			st.GapY = webLength(fields[1], st.FontPx)
		}
	}
	if v, ok := decls["column-gap"]; ok {
		st.GapX = webLength(v, st.FontPx)
	}
	if v, ok := decls["row-gap"]; ok {
		st.GapY = webLength(v, st.FontPx)
	}
	if v, ok := decls["flex-grow"]; ok {
		if grow, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && grow >= 0 {
			st.FlexGrow = grow
		}
	}
	if v, ok := decls["flex-basis"]; ok {
		webFlexBasis(st, v)
	}
	if v, ok := decls["flex"]; ok {
		webFlexShorthand(st, v)
	}
	if v, ok := decls["width"]; ok {
		if pct, valid := webPercentValue(v); valid {
			st.WidthPct = pct
		} else if px := webLength(v, st.FontPx); px > 0 {
			st.Width = px
		}
	}
	if v, ok := decls["position"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "relative":
			st.Position = webPositionRelative
		case "absolute", "fixed", "sticky":
			// The reader has no sticky scrolling; a fixed or sticky element
			// keeps its place on the page and is stacked like an absolute one.
			st.Position = webPositionAbsolute
		}
	}
	if v, ok := decls["z-index"]; ok {
		if z, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			st.ZIndex = z
		}
	}
	for _, edge := range []struct {
		key  string
		val  *float64
		flag *bool
	}{
		{"top", &st.Top, &st.HasTop},
		{"right", &st.Right, &st.HasRight},
		{"bottom", &st.Bottom, &st.HasBottom},
		{"left", &st.Left, &st.HasLeft},
	} {
		v, ok := decls[edge.key]
		if !ok {
			continue
		}
		if px := webLength(v, st.FontPx); px != 0 {
			*edge.val = px
			*edge.flag = true
		}
	}
}

// webFlexBasis reads flex-basis, keeping a percentage unresolved: the layout
// resolves it against the container's width.
func webFlexBasis(st *webStyle, value string) {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "auto" || lower == "content" || lower == "max-content" || lower == "min-content" {
		st.FlexBasis = -1
		st.BasisPct = 0
		return
	}
	if pct, ok := webPercentValue(value); ok {
		st.BasisPct = pct
		st.FlexBasis = -1
		return
	}
	st.FlexBasis = webLength(value, st.FontPx)
	st.BasisPct = 0
}

// webFlexShorthand reads the common `flex: 1`, `flex: 1 1 200px` and
// `flex: 0 0 33%` forms. The shrink factor has no meaning in a one-pass text
// layout, so it is accepted and ignored.
func webFlexShorthand(st *webStyle, value string) {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch lower {
	case "none":
		st.FlexGrow = 0
		st.FlexBasis = -1
		return
	case "auto", "initial":
		st.FlexGrow = 1
		st.FlexBasis = -1
		return
	}
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return
	}
	if grow, err := strconv.ParseFloat(fields[0], 64); err == nil {
		st.FlexGrow = grow
	} else if pct, ok := webPercentValue(fields[0]); ok {
		// `flex: 33%` is a basis, not a grow factor.
		st.BasisPct, st.FlexBasis = pct, -1
		return
	}
	if len(fields) > 2 {
		webFlexBasis(st, fields[2])
		return
	}
	if len(fields) == 2 {
		if pct, ok := webPercentValue(fields[1]); ok {
			st.BasisPct, st.FlexBasis = pct, -1
		} else if px := webLength(fields[1], st.FontPx); px > 0 {
			st.FlexBasis = px
		}
		return
	}
	// A bare number means "grow from zero": `flex: 1` is `1 1 0%`.
	if st.FlexBasis == 0 && st.BasisPct == 0 {
		st.FlexBasis = 0
	}
}

func webPercentValue(value string) (float64, bool) {
	v := strings.TrimSpace(strings.ToLower(value))
	if !strings.HasSuffix(v, "%") {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f, true
}

func webJustifyValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "center":
		return webJustifyCenter
	case "flex-end", "end", "right":
		return webJustifyEnd
	case "space-between":
		return webJustifyBetween
	case "space-around", "space-evenly":
		return webJustifyAround
	}
	return webJustifyStart
}

func webAlignValue(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "center":
		return webItemCenter
	case "flex-end", "end":
		return webItemEnd
	case "stretch":
		return webItemStretch
	}
	return webItemStart
}

// --- HTML parsing ---------------------------------------------------------

type webParser struct {
	doc       *webDocument
	rules     []webRule
	css       strings.Builder
	stack     []webFrame
	blocks    []webBlock
	current   *webBlock
	lists     []webListState
	inHead    bool
	textLen   int
	trunc     bool
	space     bool
	pendingB  string
	mainDepth int
	flexSeq   int
	flexStack []webFlexFrame
}

// parseWebPage parses HTML plus any already-fetched CSS into a document.
func parseWebPage(source, extraCSS, baseURL string) *webDocument {
	doc := &webDocument{
		URL:   baseURL,
		Paper: sdl.Color{R: 250, G: 251, B: 253, A: 255},
		Text:  sdl.Color{R: 28, G: 32, B: 38, A: 255},
	}
	p := &webParser{doc: doc}
	if extraCSS != "" {
		p.css.WriteString(extraCSS)
		p.rules = parseWebCSS(extraCSS)
	}
	p.walk(source)
	p.flush()
	doc.Blocks = filterWebMainContent(p.blocks)
	if p.trunc {
		// The note goes in after the <main> filter, and at the top. Tagging it
		// as main content made the filter keep only this one sentence and drop
		// every real block, so an article past the text budget drew as a
		// single line; left at the end it would sit below thousands of rows
		// that the layout never reaches.
		note := webBlock{
			Style: webStyle{FontPx: 13, Italic: true},
			Runs:  []webRun{{Text: "[Page content truncated so the handheld keeps its memory.]", Style: webStyle{FontPx: 13, Italic: true}}},
		}
		doc.Blocks = append([]webBlock{note}, doc.Blocks...)
	}
	return doc
}

// filterWebMainContent drops navigation, headers and footers when the page
// marks its real content with <main> or <article>. Reading a docs page should
// start at the docs, not at the site's menu.
func filterWebMainContent(blocks []webBlock) []webBlock {
	hasMain := false
	for _, block := range blocks {
		if block.InMain {
			hasMain = true
			break
		}
	}
	if !hasMain {
		return blocks
	}
	kept := blocks[:0]
	for _, block := range blocks {
		if block.InMain {
			kept = append(kept, block)
		}
	}
	return kept
}

// fetchWebStylesheets pulls the page's linked stylesheets. External CSS is what
// makes most real pages look like themselves, so a few small sheets are worth
// the request; large frameworks are capped and simply contribute nothing.
func fetchWebStylesheets(source string, base *url.URL) string {
	if base == nil {
		return ""
	}
	var addresses []string
	seen := make(map[string]bool)
	pos := 0
	for pos < len(source) && len(addresses) < webMaxStylesheets {
		rel := webIndexFold(source[pos:], "<link")
		if rel < 0 {
			break
		}
		start := pos + rel
		end := findTextBrowserTagEnd(source, start+1)
		if end < 0 {
			break
		}
		tag := source[start+1 : end]
		pos = end + 1
		relAttr := strings.ToLower(htmlTagAttribute(tag, "rel"))
		if !strings.Contains(relAttr, "stylesheet") {
			continue
		}
		href := strings.TrimSpace(htmlTagAttribute(tag, "href"))
		if href == "" {
			continue
		}
		ref, err := url.Parse(href)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		if abs.Scheme != "http" && abs.Scheme != "https" || seen[abs.String()] {
			continue
		}
		seen[abs.String()] = true
		addresses = append(addresses, abs.String())
	}
	if len(addresses) == 0 {
		return ""
	}
	// Fetch the sheets side by side. One slow sheet used to hold up the whole
	// page load, and on handheld wifi three sequential timeouts could keep the
	// reader on "Loading ..." for the better part of a minute.
	bodies := make([]string, len(addresses))
	var wg sync.WaitGroup
	pageURL := base.String()
	for i, address := range addresses {
		wg.Add(1)
		go func(i int, address string) {
			defer wg.Done()
			bodies[i] = fetchWebStylesheet(pageURL, address)
		}(i, address)
	}
	wg.Wait()
	var out strings.Builder
	for _, css := range bodies {
		if css == "" || out.Len() >= webMaxCSSBytes {
			continue
		}
		out.WriteString(css)
		out.WriteString("\n")
	}
	return out.String()
}

func fetchWebStylesheet(pageURL, address string) string {
	// A stylesheet from an ad or analytics host is never part of what the
	// reader draws; skipping it saves the request, not just the bytes. A page
	// the user excepted fetches everything.
	if browserRequestBlocked(pageURL, address) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "JukaHub Browser/1.1")
	// Same jar as the page: a sheet served only to a session (a CDN that
	// hands out a token with the HTML) has to see the page's cookie.
	client := browserHTTPClient(6 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.Contains(ct, "css") {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webSheetByteCap+1))
	if err != nil || len(body) > webSheetByteCap {
		return ""
	}
	return string(body)
}

func webIndexFold(source, sub string) int {
	for i := 0; i+len(sub) <= len(source); i++ {
		if strings.EqualFold(source[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// webRawElement returns the raw text of a style/title/script-like element and
// the offset just past its closing tag.
func webRawElement(source string, start int, tag string) (string, int, bool) {
	closing := "</" + tag
	idx := webIndexFold(source[start:], closing)
	if idx < 0 {
		return source[start:], len(source), true
	}
	body := source[start : start+idx]
	end := start + idx + len(closing)
	if gt := strings.IndexByte(source[end:], '>'); gt >= 0 {
		end += gt + 1
	} else {
		end = len(source)
	}
	return body, end, true
}

func parseWebTag(raw string) (name string, closing, selfClose bool, attrs string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, false, ""
	}
	if raw[0] == '/' {
		closing = true
		raw = strings.TrimSpace(raw[1:])
	}
	if strings.HasSuffix(raw, "/") {
		selfClose = true
		raw = strings.TrimSpace(raw[:len(raw)-1])
	}
	end := 0
	for end < len(raw) && webSelectorNameByte(raw[end]) {
		end++
	}
	name = strings.ToLower(raw[:end])
	attrs = raw[end:]
	return name, closing, selfClose, attrs
}

func webVoidElement(tag string) bool {
	switch tag {
	case "br", "hr", "img", "meta", "link", "input", "area", "base", "col",
		"embed", "param", "source", "track", "wbr":
		return true
	}
	return false
}

func webRawTextElement(tag string) bool {
	switch tag {
	case "script", "noscript", "svg", "iframe", "template", "canvas", "video", "audio", "object":
		return true
	}
	return false
}

func webBlockElement(tag string) bool {
	switch tag {
	case "body", "p", "div", "section", "article", "main", "header", "footer",
		"aside", "nav", "form", "fieldset", "figure", "figcaption", "address",
		"dl", "dt", "dd", "li", "tr", "details", "summary", "blockquote", "pre",
		"h1", "h2", "h3", "h4", "h5", "h6":
		return true
	}
	return false
}

func (p *webParser) walk(source string) {
	for i := 0; i < len(source); {
		if source[i] != '<' {
			end := strings.IndexByte(source[i:], '<')
			if end < 0 {
				end = len(source)
			} else {
				end += i
			}
			p.addText(source[i:end])
			i = end
			continue
		}
		if strings.HasPrefix(source[i:], "<!--") {
			if end := strings.Index(source[i+4:], "-->"); end >= 0 {
				i += 4 + end + 3
			} else {
				i = len(source)
			}
			continue
		}
		if strings.HasPrefix(source[i:], "<!") || strings.HasPrefix(source[i:], "<?") {
			if end := strings.IndexByte(source[i:], '>'); end >= 0 {
				i += end + 1
			} else {
				i = len(source)
			}
			continue
		}
		end := findTextBrowserTagEnd(source, i+1)
		if end < 0 {
			break
		}
		raw := source[i+1 : end]
		i = end + 1
		name, closing, selfClose, attrs := parseWebTag(raw)
		if name == "" {
			continue
		}
		if closing {
			p.endTag(name)
			continue
		}
		if name == "title" || name == "style" || webRawTextElement(name) {
			inner, next, _ := webRawElement(source, i, name)
			i = next
			switch name {
			case "title":
				if p.doc.Title == "" {
					p.doc.Title = cleanTextBrowserHTML(inner)
				}
			case "style":
				if p.css.Len()+len(inner) < webMaxCSSBytes {
					p.css.WriteString("\n")
					p.css.WriteString(inner)
					p.rules = parseWebCSS(p.css.String())
				}
			case "video", "iframe":
				p.addVideoBlock(name, attrs, inner)
			}
			continue
		}
		p.startTag(name, attrs, selfClose)
	}
}

func (p *webParser) startTag(name, attrs string, selfClose bool) {
	if name == "head" {
		p.inHead = true
		p.push(webElement{tag: name}, webBaseStyle(), "skip")
		return
	}
	if name == "body" || name == "html" {
		p.inHead = false
	}
	el := webElement{tag: name}
	el.id = strings.TrimSpace(htmlTagAttribute(attrs, "id"))
	el.classes = strings.Fields(htmlTagAttribute(attrs, "class"))
	style := p.computeStyle(el, attrs)
	if name == "body" || name == "html" {
		if style.HasBg {
			p.doc.Paper = style.Background
			p.doc.HasPaperGradient = style.HasGradient && style.Gradient != nil
			if p.doc.HasPaperGradient {
				p.doc.PaperGradient = *style.Gradient
			}
		}
		if style.HasColor {
			p.doc.Text = style.Color
		}
	}

	if name == "main" || name == "article" {
		p.mainDepth++
	}

	switch name {
	case "br":
		p.addBreak()
		return
	case "hr":
		p.flush()
		// The rule keeps its authored colour and weight: `hr { border: 3px
		// solid #c00 }` used to paint the reader's grey hairline as if the
		// sheet had said nothing, which is the one thing it did say.
		p.blocks = append(p.blocks, webBlock{
			Rule:           true,
			MarginTop:      style.MarginTop,
			MarginBot:      style.MarginBot,
			BorderColor:    style.BorderColor,
			HasBorderColor: style.HasBorderColor,
			BorderWidth:    style.BorderWidth,
			HasBg:          style.HasBg,
			Background:     style.Background,
			Indent:         style.Indent,
		})
		return
	case "img":
		if p.inHead {
			return
		}
		src := strings.TrimSpace(htmlTagAttribute(attrs, "src"))
		alt := strings.TrimSpace(htmlTagAttribute(attrs, "alt"))
		resolved := p.webImageURL(src)
		if resolved == "" {
			// A dead or undecodable src keeps its alt text; a loadable one
			// becomes a real image block below.
			if alt != "" {
				p.addText(" " + alt + " ")
			}
			return
		}
		p.flush()
		block := p.ensureBlock()
		block.Image = true
		block.ImageSrc = resolved
		block.ImageW = htmlTagAttributeFloat(attrs, "width")
		block.ImageH = htmlTagAttributeFloat(attrs, "height")
		p.flush()
		return
	case "td", "th":
		if p.current != nil && len(p.current.Runs) > 0 {
			p.addText("   ")
		}
	}
	if webVoidElement(name) {
		return
	}
	if name == "ul" || name == "ol" {
		p.flush()
		p.lists = append(p.lists, webListState{ordered: name == "ol"})
		p.push(el, style, "list")
		return
	}
	if name == "li" {
		p.flush()
		p.pendingB = p.nextBullet(style)
		p.push(el, style, "block")
		return
	}
	if webBlockElement(name) {
		p.flush()
		p.push(el, style, "block")
		if selfClose {
			p.endTag(name)
		}
		return
	}
	p.push(el, style, "inline")
	if selfClose {
		p.endTag(name)
	}
}

func (p *webParser) endTag(name string) {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].el.tag != name {
			continue
		}
		if p.stack[i].kind == "block" || p.stack[i].kind == "list" {
			p.flush()
		}
		if name == "head" {
			p.inHead = false
		}
		if (name == "main" || name == "article") && p.mainDepth > 0 {
			p.mainDepth--
		}
		if name == "ul" || name == "ol" {
			if depth := len(p.lists); depth > 0 {
				p.lists = p.lists[:depth-1]
			}
		}
		if name == "li" {
			p.pendingB = ""
		}
		// Closing an element also closes every flex container it opened, even
		// when its own end tag was missing (real pages are full of those).
		for len(p.flexStack) > 0 && p.flexStack[len(p.flexStack)-1].depth >= i {
			p.flexStack = p.flexStack[:len(p.flexStack)-1]
		}
		p.stack = p.stack[:i]
		return
	}
}

func (p *webParser) push(el webElement, style webStyle, kind string) {
	depth := len(p.stack)
	// A block element pushed directly under an open flex container is one of
	// its items; nested elements belong to the same item. Text-only children
	// are tagged by ensureBlock instead, which is why the counter starts at
	// zero rather than one.
	if (kind == "block" || kind == "list") && len(p.flexStack) > 0 {
		// The container was pushed at its own depth, so its direct children
		// are pushed one level deeper. Comparing equal depths never matched,
		// which left every item in one column.
		if top := &p.flexStack[len(p.flexStack)-1]; top.depth+1 == depth {
			top.item++
		}
	}
	group := 0
	if (kind == "block" || kind == "list") && style.Display == webDisplayFlex {
		p.flexSeq++
		group = p.flexSeq
		p.flexStack = append(p.flexStack, webFlexFrame{
			group:   group,
			depth:   depth,
			dir:     style.FlexDir,
			justify: style.Justify,
			align:   style.AlignItems,
			gapX:    style.GapX,
			gapY:    style.GapY,
			wrap:    style.FlexWrap,
		})
	}
	p.stack = append(p.stack, webFrame{el: el, style: style, kind: kind, flexGroup: group})
}

func (p *webParser) topStyle() webStyle {
	if len(p.stack) == 0 {
		return webBaseStyle()
	}
	return p.stack[len(p.stack)-1].style
}

// nearestBlockStyle is the style of the block element the text belongs to.
func (p *webParser) nearestBlockStyle() webStyle {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].kind == "block" {
			return p.stack[i].style
		}
	}
	return p.topStyle()
}

func (p *webParser) computeStyle(el webElement, attrs string) webStyle {
	st := p.topStyle()
	// Non-inherited properties start clean for every element.
	st.HasBg, st.Background = false, sdl.Color{}
	st.HasGradient, st.Gradient = false, nil
	st.Indent, st.TextIndent, st.MarginTop, st.MarginBot = 0, 0, 0, 0
	st.BorderLeft, st.BorderRight, st.ListNone = false, false, false
	st.BorderWidth = 0
	// A radius and a right gutter belong to the element that declared them;
	// line-height, the list marker and the opacity group are inherited above.
	st.Radius, st.IndentRight = 0, 0
	st.BoxBorder, st.BorderTop, st.BorderBottom = false, false, false
	st.BorderColor, st.HasBorderColor = sdl.Color{}, false
	// Layout properties are per element too: a child of a flex container is
	// not itself a container, and an item's grow factor belongs to it alone.
	st.Display, st.FlexDir = webDisplayBlock, webFlexRow
	st.Justify, st.AlignItems, st.AlignSelf = webJustifyStart, webItemStretch, -1
	st.GapX, st.GapY, st.FlexWrap = 0, 0, false
	st.FlexGrow, st.FlexBasis, st.BasisPct = 0, -1, 0
	st.Width, st.WidthPct = 0, 0
	st.ZIndex, st.Position = 0, webPositionStatic
	st.Top, st.Right, st.Bottom, st.Left = 0, 0, 0, 0
	st.HasTop, st.HasRight, st.HasBottom, st.HasLeft = false, false, false, false
	// Link colour, underline and strikethrough are not reset: a decoration
	// on <a> or <s> has to keep showing on the <strong>/<span> inside it,
	// which is how every browser renders `<a><b>text</b></a>`.

	applyWebTagDefaults(&st, el.tag)

	chain := make([]webElement, 0, len(p.stack)+1)
	for _, frame := range p.stack {
		chain = append(chain, frame.el)
	}
	chain = append(chain, el)

	var matched []webRule
	for _, rule := range p.rules {
		if webSelectorMatches(rule.selector, chain) {
			matched = append(matched, rule)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].specificity != matched[j].specificity {
			return matched[i].specificity < matched[j].specificity
		}
		return matched[i].order < matched[j].order
	})
	// Cascade: every normal declaration in specificity order, then the
	// !important ones. Collapsing the winners into one map also gives var()
	// and calc() the final value to resolve against, so the property parsers
	// below only ever see plain lengths and colours.
	merged := make(map[string]string, 16)
	for _, rule := range matched {
		for name, value := range rule.decls {
			merged[name] = value
		}
	}
	for _, rule := range matched {
		for name, value := range rule.important {
			merged[name] = value
		}
	}
	if inline := htmlTagAttribute(attrs, "style"); inline != "" {
		inlineDecls, inlineImportant := parseWebDeclsSplit(inline)
		for name, value := range inlineDecls {
			merged[name] = value
		}
		for name, value := range inlineImportant {
			merged[name] = value
		}
	}
	// Custom properties are inherited, so this element's own definitions are
	// resolved once here and carried down by the frames below it.
	st.Vars = webVarScopeFor(p.topStyle().Vars, merged)
	for name := range merged {
		if strings.HasPrefix(name, "--") {
			delete(merged, name)
		}
	}
	// `em` inside calc() is this element's font size, which font-size itself
	// has to settle first.
	em := st.FontPx
	if raw, ok := merged["font-size"]; ok {
		if px, valid := webFontSizePx(webResolveValue(raw, st.Vars, em), em); valid {
			em = px
		}
	}
	for name, value := range merged {
		merged[name] = webResolveValue(value, st.Vars, em)
	}
	applyWebDecls(&st, merged)
	// The anchor target is not a CSS property and is never inherited from a
	// sibling: every <a> names its own, and its children inherit that.
	if el.tag == "a" {
		st.Href = htmlstd.UnescapeString(strings.TrimSpace(htmlTagAttribute(attrs, "href")))
	}
	// Decorative icon fonts and aria-hidden helpers render as short words like
	// "navigate_next" or "menu" in a text engine; hide them.
	if strings.EqualFold(strings.TrimSpace(htmlTagAttribute(attrs, "aria-hidden")), "true") {
		st.Hidden = true
	}
	for _, class := range el.classes {
		switch strings.ToLower(class) {
		case "material-icons", "material-icons-outlined", "material-symbols",
			"material-symbols-outlined", "material-symbols-rounded", "material-symbols-sharp":
			st.Hidden = true
		}
	}
	return st
}

func (p *webParser) nextBullet(style webStyle) string {
	if len(p.lists) == 0 || style.ListNone {
		return ""
	}
	list := &p.lists[len(p.lists)-1]
	if list.ordered {
		list.n++
	}
	return webListMarker(style.ListStyle, list.ordered, list.n)
}

func (p *webParser) ensureBlock() *webBlock {
	if p.current != nil {
		return p.current
	}
	st := p.nearestBlockStyle()
	block := &webBlock{
		Style:          st,
		Align:          st.Align,
		Indent:         st.Indent,
		TextIndent:     st.TextIndent,
		MarginTop:      st.MarginTop,
		MarginBot:      st.MarginBot,
		Pre:            st.Pre,
		HasBg:          st.HasBg,
		Background:     st.Background,
		BorderLeft:     st.BorderLeft,
		BorderRight:    st.BorderRight,
		BoxBorder:      st.BoxBorder,
		BorderTop:      st.BorderTop,
		BorderBottom:   st.BorderBottom,
		BorderWidth:    st.BorderWidth,
		BorderColor:    st.BorderColor,
		HasBorderColor: st.HasBorderColor,
		Bullet:         p.pendingB,
		InMain:         p.mainDepth > 0,
		FlexGrow:       st.FlexGrow,
		FlexBasis:      st.FlexBasis,
		BasisPct:       st.BasisPct,
		Width:          st.Width,
		WidthPct:       st.WidthPct,
		AlignSelf:      st.AlignSelf,
		ZIndex:         st.ZIndex,
		Float:          st.Position == webPositionAbsolute,
		PosTop:         st.Top,
		PosRight:       st.Right,
		PosBottom:      st.Bottom,
		PosLeft:        st.Left,
		HasTop:         st.HasTop,
		HasRight:       st.HasRight,
		HasBottom:      st.HasBottom,
		HasLeft:        st.HasLeft,
	}
	// The ramp is copied by value onto the block: a background belongs to the
	// element that declared it, unlike the shared pointer the style carries for
	// comparison.
	if st.HasGradient && st.Gradient != nil {
		block.Gradient, block.HasGradient = *st.Gradient, true
	}
	// The innermost open flex container owns this block. A container's own
	// leading text gets item 1, the same slot the first child would take; a
	// block that starts a fresh element has already bumped the counter.
	if n := len(p.flexStack); n > 0 {
		f := &p.flexStack[n-1]
		if f.item == 0 {
			f.item = 1
		}
		block.FlexGroup = f.group
		block.FlexItem = f.item
		block.FlexDir = f.dir
		block.FlexJustify = f.justify
		block.FlexAlign = f.align
		block.FlexGapX = f.gapX
		block.FlexGapY = f.gapY
		block.FlexWrap = f.wrap
	}
	p.pendingB = ""
	p.current = block
	return block
}

func (p *webParser) addBreak() {
	block := p.ensureBlock()
	style := p.topStyle()
	block.Runs = append(block.Runs, webRun{Text: "\n", Style: style})
	p.space = false
}

func (p *webParser) addText(raw string) {
	if p.inHead || raw == "" {
		return
	}
	style := p.topStyle()
	if style.Hidden {
		return
	}
	if p.textLen >= webMaxTextBytes || len(p.blocks) >= webMaxBlocks {
		p.trunc = true
		return
	}
	text := strings.ToValidUTF8(htmlstd.UnescapeString(raw), "")
	if style.Pre {
		text = strings.ReplaceAll(text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		if text == "" {
			return
		}
		p.appendRun(text, style)
		return
	}
	collapsed := strings.Join(strings.Fields(text), " ")
	if collapsed == "" {
		p.space = true
		return
	}
	// Whitespace can sit on either side of a tag boundary: `<b>x</b> next`
	// starts the following text node with a space, and that space has to
	// survive so the layout does not glue "xnext" together.
	leadsWithSpace := false
	if r, _ := utf8.DecodeRuneInString(text); unicode.IsSpace(r) {
		leadsWithSpace = true
	}
	block := p.ensureBlock()
	if (p.space || leadsWithSpace) && len(block.Runs) > 0 {
		last := block.Runs[len(block.Runs)-1].Text
		if last != "" && !strings.HasSuffix(last, " ") && !strings.HasSuffix(last, "\n") {
			collapsed = " " + collapsed
		}
	}
	p.space = false
	trimmed := strings.TrimRight(text, " \t\r\n")
	if len(trimmed) < len(text) {
		p.space = true
	}
	p.appendRun(collapsed, style)
}

func (p *webParser) appendRun(text string, style webStyle) {
	if text == "" {
		return
	}
	if p.textLen >= webMaxTextBytes {
		p.trunc = true
		return
	}
	if remaining := webMaxTextBytes - p.textLen; len(text) > remaining {
		// Keep the part that fits and flag the page as truncated; dropping the
		// whole run would lose the text that came before the cap.
		cut := remaining
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
		p.trunc = true
		if text == "" {
			return
		}
	}
	block := p.ensureBlock()
	if n := len(block.Runs); n > 0 && block.Runs[n-1].Style == style {
		block.Runs[n-1].Text += text
	} else {
		block.Runs = append(block.Runs, webRun{Text: text, Style: style})
	}
	p.textLen += len(text)
}

func (p *webParser) flush() {
	if p.current == nil {
		return
	}
	block := p.current
	p.current = nil
	// Drop a block that only carried whitespace.
	hasText := false
	for _, run := range block.Runs {
		if strings.TrimSpace(strings.ReplaceAll(run.Text, "\n", "")) != "" {
			hasText = true
			break
		}
	}
	if !hasText && block.Bullet == "" && !block.Rule && !block.Image && !block.Video {
		return
	}
	for i := range block.Runs {
		if i == len(block.Runs)-1 {
			block.Runs[i].Text = strings.TrimRight(block.Runs[i].Text, " ")
		}
	}
	p.blocks = append(p.blocks, *block)
}

// --- Layout ---------------------------------------------------------------

// webBaseFontSize mirrors getCachedFont's default so CSS px map onto the same
// body text size the rest of the UI uses.
func webBaseFontSize(config *Config, fontName string) float64 {
	if config != nil {
		if size, ok := config.Variables.FontSizes[fontName]; ok && size > 0 {
			return float64(size)
		}
	}
	return 24
}

// layoutFor returns the measured page, reusing the previous layout while the
// width, font and page colours are unchanged.
// webGradientStamp fingerprints a ramp for the layout cache. A page whose paper
// changed from one gradient to another keeps the same average colour, so the
// colours alone would let a stale layout through with the old ramp baked into
// its rects.
func webGradientStamp(g webGradient, has bool) string {
	if !has || len(g.Stops) == 0 {
		return "-"
	}
	first, last := g.Stops[0].Color, g.Stops[len(g.Stops)-1].Color
	axis := "h"
	if g.Vertical {
		axis = "v"
	}
	return fmt.Sprintf("%s%d:%02x%02x%02x-%02x%02x%02x", axis, len(g.Stops),
		first.R, first.G, first.B, last.R, last.G, last.B)
}

func (d *webDocument) layoutFor(config *Config, elem Element, width int32) *webLayout {
	stamp := fmt.Sprintf("%d|%s|%d|%d,%d,%d|%d,%d,%d|%s",
		width, elem.Font, a11yScaleFontSize(16),
		d.Paper.R, d.Paper.G, d.Paper.B,
		d.Text.R, d.Text.G, d.Text.B,
		webGradientStamp(d.PaperGradient, d.HasPaperGradient))
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.layout != nil && d.stamp == stamp {
		return d.layout
	}
	d.layout = d.computeLayout(config, elem, width)
	d.stamp = stamp
	return d.layout
}

func (d *webDocument) computeLayout(config *Config, elem Element, width int32) *webLayout {
	if width < 80 {
		return nil
	}
	baseSize := webBaseFontSize(config, elem.Font)
	baseFont := getFontSized(config, elem.Font, int(baseSize))
	if baseFont == nil {
		return nil
	}
	rowH := int32(baseFont.Height()) + 6
	if rowH < 18 {
		rowH = 18
	}
	layout := &webLayout{RowH: rowH}

	fonts := make(map[int]*ttf.Font, 8)
	fontAt := func(cssPx float64) *ttf.Font {
		size := int(math.Round(cssPx * baseSize / 16))
		minSize := int(math.Round(baseSize * 0.72))
		maxSize := int(math.Round(baseSize * 1.5))
		if size < minSize {
			size = minSize
		}
		if size > maxSize {
			size = maxSize
		}
		if size < 8 {
			size = 8
		}
		if font, ok := fonts[size]; ok {
			return font
		}
		font := getFontSized(config, elem.Font, size)
		fonts[size] = font
		return font
	}

	// Cap the measure the way a real content column does. Full-bleed lines on
	// a 1280px panel run to ~140 characters, which is tiring on a small
	// screen; 38 em keeps a comfortable line and centres the column.
	contentW := width - 12
	if limit := int32(baseSize * 38); limit > 0 && contentW > limit {
		contentW = limit
	}
	if contentW < 60 {
		return nil
	}
	padX := (width - contentW) / 2
	l := &webLayouter{
		doc: d, layout: layout, rowH: rowH, baseSize: baseSize, fontAt: fontAt,
		spaceWidths: map[*ttf.Font]int32{},
		sanitized:   make(map[webSanitizeKey]string, 256),
	}
	// Absolutely positioned blocks are measured on their own and painted over
	// the flow, so they take no room in it.
	flow := make([]webBlock, 0, len(d.Blocks))
	var floats []webBlock
	for _, b := range d.Blocks {
		if b.Float {
			floats = append(floats, b)
			continue
		}
		flow = append(flow, b)
	}
	l.layoutBlocks(flow, padX, contentW)

	layout.Height = int32(len(layout.Rows)) * rowH
	l.layoutFloats(floats, padX, contentW)
	sort.SliceStable(layout.Rects, func(i, j int) bool { return layout.Rects[i].Z < layout.Rects[j].Z })
	layout.Links = collectWebLinks(layout)
	// Every play card is also a link the browser walks and activates; the
	// merge keeps rows in reading order so the video sits where it drew.
	if vids := collectWebVideoLinks(layout); len(vids) > 0 {
		layout.Links = append(layout.Links, vids...)
		sort.SliceStable(layout.Links, func(i, j int) bool {
			return layout.Links[i].Row < layout.Links[j].Row
		})
	}
	return layout
}

// webLayouter is the geometry shared by the page and every flex item inside it:
// the row grid, the font mapper and the document whose colours the runs use. A
// flex item is laid out by a second webLayouter into its own small webLayout,
// which is then merged into the page at an offset.
type webLayouter struct {
	doc      *webDocument
	layout   *webLayout
	rowH     int32
	baseSize float64
	fontAt   func(float64) *ttf.Font

	// Measured facts reused within one layout. Wrapping asks for the width of
	// a space once per word and for the same words over and over, and both
	// TTF_SizeUTF8 and the missing-glyph scan cost real time on a 1-core
	// handheld: the caches cut the work without changing the layout.
	spaceWidths map[*ttf.Font]int32
	sanitized   map[webSanitizeKey]string
}

// webSanitizeKey identifies one word for the sanitize cache.
type webSanitizeKey struct {
	font *ttf.Font
	text string
}

// spaceWidth returns the width of one space in font, measured once per layout.
func (l *webLayouter) spaceWidth(font *ttf.Font) int32 {
	if w, ok := l.spaceWidths[font]; ok {
		return w
	}
	w := int32(6)
	if sw, _, err := font.SizeUTF8(" "); err == nil {
		w = int32(sw)
	}
	if l.spaceWidths == nil {
		l.spaceWidths = map[*ttf.Font]int32{}
	}
	l.spaceWidths[font] = w
	return w
}

// sanitizedWord drops characters the font cannot draw, remembering the result
// so the same word is scanned once.
func (l *webLayouter) sanitizedWord(font *ttf.Font, text string) string {
	key := webSanitizeKey{font: font, text: text}
	if out, ok := l.sanitized[key]; ok {
		return out
	}
	out := sanitizeForFont(font, text)
	if l.sanitized == nil {
		l.sanitized = make(map[webSanitizeKey]string, 256)
	}
	l.sanitized[key] = out
	return out
}

// layoutBlocks lays out a column of blocks into l.layout. Positions are
// relative to the column's own width, with padX as its left edge, so a flex
// item can be laid out at 0 and moved later.
func (l *webLayouter) layoutBlocks(blocks []webBlock, padX, contentW int32) {
	layout := l.layout
	rowH := l.rowH
	baseSize := l.baseSize
	fontAt := l.fontAt
	d := l.doc
	nextY := func() int32 { return int32(len(layout.Rows)) * rowH }
	skipRows := func(n int32) {
		for i := int32(0); i < n && len(layout.Rows) < webMaxRows; i++ {
			layout.Rows = append(layout.Rows, webRow{Y: nextY()})
		}
	}
	emit := func(runs []webRunPos, fontH int32) {
		if len(layout.Rows) >= webMaxRows {
			return
		}
		slots := int32(1)
		if need := int32(math.Ceil(float64(fontH+4) / float64(rowH))); need > slots {
			slots = need
		}
		idx := int32(len(layout.Rows))
		y := idx * rowH
		if slots > 1 {
			y += (slots*rowH - fontH) / 2
		}
		layout.Rows = append(layout.Rows, webRow{Y: y, Runs: runs})
		for i := int32(1); i < slots && len(layout.Rows) < webMaxRows; i++ {
			layout.Rows = append(layout.Rows, webRow{Y: (idx + i) * rowH})
		}
	}
	marginRows := func(cssPx float64) int32 {
		if cssPx <= 0 {
			return 0
		}
		design := cssPx * baseSize / 16
		rows := int32(math.Round(design / float64(rowH)))
		if rows < 1 && design >= float64(rowH)*0.45 {
			rows = 1
		}
		if rows > 3 {
			rows = 3
		}
		return rows
	}

	// Adjacent vertical margins collapse to the larger of the two, the way CSS
	// does it. Summing them stacked a heading's own spacing on top of the
	// previous block's and filled real pages with holes on a small screen.
	previousMarginBot := 0.0
	lastFlexItem := 0
	for i := 0; i < len(blocks); i++ {
		block := blocks[i]
		if len(layout.Rows) >= webMaxRows {
			break
		}
		// A run of blocks tagged with one flex group is that container's
		// items. In a row they become side-by-side columns; in a column they
		// are ordinary blocks separated by the container's gap.
		if block.FlexGroup != 0 && block.FlexDir == webFlexRow {
			j := i
			for j < len(blocks) && blocks[j].FlexGroup == block.FlexGroup && blocks[j].FlexDir == webFlexRow {
				j++
			}
			gap := block.MarginTop
			if previousMarginBot > gap {
				gap = previousMarginBot
			}
			if len(layout.Rows) > 0 {
				skipRows(marginRows(gap))
			}
			l.layoutFlexRow(blocks[i:j], padX, contentW)
			previousMarginBot = 0
			i = j - 1
			continue
		}
		if block.FlexGroup != 0 && block.FlexDir == webFlexColumn &&
			block.FlexItem != lastFlexItem && lastFlexItem != 0 {
			skipRows(marginRows(block.FlexGapY))
		}
		lastFlexItem = block.FlexItem
		gap := block.MarginTop
		if previousMarginBot > gap {
			gap = previousMarginBot
		}
		// No leading blank rows at the top of the page.
		if len(layout.Rows) > 0 {
			skipRows(marginRows(gap))
		}
		previousMarginBot = block.MarginBot
		if block.Rule {
			y := nextY() + rowH/2
			color := sdl.Color{R: 200, G: 206, B: 216, A: 255}
			switch {
			case block.HasBorderColor:
				color = block.BorderColor
			case block.HasBg:
				// `hr { background: #eee }` is the other common spelling of a
				// rule, and browsers paint it the same way.
				color = block.Background
			}
			barW := int32(math.Round(block.BorderWidth))
			if barW <= 0 {
				barW = 2
			}
			if barW > 12 {
				barW = 12
			}
			indent := int32(block.Indent * baseSize / 16)
			if indent < 0 || indent > contentW/2 {
				indent = 0
			}
			layout.Rects = append(layout.Rects, webRect{
				Rect:  sdl.Rect{X: padX + indent, Y: y, W: contentW - indent, H: barW},
				Color: webFadeColor(color, d.Paper, block.Style),
			})
			skipRows(1)
			continue
		}
		if block.Image {
			l.layoutImage(block, padX, contentW)
			continue
		}
		if block.Video {
			l.layoutVideo(block, padX, contentW)
			continue
		}
		startGrid := int32(len(layout.Rows))

		indent := int32(block.Indent * baseSize / 16)
		if indent < 0 {
			indent = 0
		}
		if indent > contentW/2 {
			indent = contentW / 2
		}
		// padding-right/margin-right narrow the measure from the other side,
		// which is what an inset card or a page gutter asks for.
		indentRight := int32(block.Style.IndentRight * baseSize / 16)
		if indentRight < 0 {
			indentRight = 0
		}
		if indentRight > contentW/3 {
			indentRight = contentW / 3
		}
		// One radius per box, in design pixels; the draw clamps it to half the
		// box so `border-radius: 999px` is a pill rather than an error.
		radius := int32(block.Style.Radius * baseSize / 16)
		if radius < 0 {
			radius = 0
		}
		// text-indent pushes only the first line, like a print paragraph.
		firstLineIndent := int32(block.TextIndent * baseSize / 16)
		if firstLineIndent < 0 {
			firstLineIndent = 0
		}
		if firstLineIndent > contentW/2 {
			firstLineIndent = contentW / 2
		}
		bullet := ""
		bulletW := int32(0)
		if block.Bullet != "" {
			bullet = block.Bullet
			if font := fontAt(16); font != nil {
				if w, _, err := font.SizeUTF8(bullet + " "); err == nil {
					bulletW = int32(w)
				}
			}
		}
		textIndent := indent + bulletW
		avail := contentW - textIndent - indentRight
		if avail < 48 {
			textIndent = indent
			avail = contentW - textIndent - indentRight
		}
		if avail < 48 {
			avail = contentW - textIndent
		}

		var line []webRunPos
		var lineMax, lineFontH int32
		firstLine := true
		lineStart := padX + textIndent
		x := lineStart + firstLineIndent

		flushLine := func() {
			if len(line) == 0 {
				return
			}
			lineWidth := lineMax - (padX + textIndent + firstLineIndent)
			if lineWidth < 0 {
				lineWidth = 0
			}
			if firstLine && bullet != "" {
				if font := fontAt(16); font != nil {
					line = append([]webRunPos{{
						Text:  bullet,
						X:     padX + indent,
						Font:  font,
						Color: webResolveColor(block.Style, d),
					}}, line...)
				}
			}
			lineAvail := avail
			if firstLine {
				lineAvail = avail - firstLineIndent
				if lineAvail < 0 {
					lineAvail = 0
				}
			}
			offset := int32(0)
			switch block.Align {
			case 1:
				offset = (lineAvail - lineWidth) / 2
			case 2:
				offset = lineAvail - lineWidth
			}
			if offset < 0 {
				offset = 0
			}
			if offset > 0 {
				for i := range line {
					line[i].X += offset
				}
			}
			// The author's line-height, when it is larger than the glyphs,
			// becomes the slot height: the row grid centres the text in the
			// slot, which is exactly what extra leading looks like. Capped so
			// `line-height: 400px` cannot spend the screen on one paragraph.
			slotH := lineFontH
			if lh := block.Style.LineHeight * baseSize / 16; lh > float64(slotH) {
				if limit := float64(rowH * 3); lh > limit {
					lh = limit
				}
				slotH = int32(lh)
			}
			emit(line, slotH)
			firstLine = false
			line = nil
			lineMax, lineFontH = 0, 0
			x = lineStart
		}

		for _, run := range block.Runs {
			segments := strings.Split(run.Text, "\n")
			for si, segment := range segments {
				if si > 0 {
					if len(line) == 0 {
						// An explicit <br> on an empty line is a blank row.
						skipRows(1)
					} else {
						flushLine()
					}
				}
				if segment == "" {
					continue
				}
				words := strings.Fields(segment)
				if block.Pre && len(words) > 0 {
					// Keep code lines as one measured run; a long one is
					// clipped to the page width instead of re-flowed.
					words = []string{strings.TrimRight(segment, " ")}
				}
				// A run only gets a space before it when the source had one.
				// Inserting one at every style boundary pushed punctuation
				// away from the word it belongs to ("price , bold ,").
				leadingSpace := strings.HasPrefix(segment, " ") || strings.HasPrefix(segment, "\t")
				for wi, word := range words {
					if word == "" {
						continue
					}
					font := fontAt(run.Style.FontPx)
					if font == nil {
						continue
					}
					word = webTransformWord(word, run.Style.Transform)
					text := l.sanitizedWord(font, word)
					if text == "" {
						continue
					}
					width, height, err := font.SizeUTF8(text)
					if err != nil {
						continue
					}
					wordW := int32(width)
					if len(line) > 0 && (wi > 0 || leadingSpace) {
						spaceW := l.spaceWidth(font)
						if x+spaceW+wordW > padX+contentW {
							flushLine()
						} else {
							x += spaceW
						}
					}
					if wordW > avail {
						text = ellipsize(font, text, avail)
						if w2, _, e := font.SizeUTF8(text); e == nil {
							wordW = int32(w2)
						}
					}
					line = append(line, webRunPos{
						Text:      text,
						X:         x,
						Font:      font,
						Color:     webResolveColor(run.Style, d),
						Bold:      run.Style.Bold,
						Underline: run.Style.Underline,
						Strike:    run.Style.Strike,
						Href:      run.Style.Href,
						HasBg:     run.Style.HasBg && !block.HasBg,
						Bg:        run.Style.Background,
					})
					x += wordW
					if x > lineMax {
						lineMax = x
					}
					if int32(height) > lineFontH {
						lineFontH = int32(height)
					}
				}
			}
		}
		flushLine()

		endGrid := int32(len(layout.Rows))
		height := (endGrid - startGrid) * rowH
		if block.HasBg && height > 0 {
			rect := webRect{
				Rect:   sdl.Rect{X: padX - 4, Y: startGrid * rowH, W: contentW + 8, H: height},
				Color:  webFadeColor(block.Background, d.Paper, block.Style),
				Z:      block.ZIndex,
				Radius: radius,
			}
			if block.HasGradient {
				rect.Gradient = webFadeGradient(block.Gradient, d.Paper, block.Style)
				rect.HasGradient = true
			}
			layout.Rects = append(layout.Rects, rect)
		}
		if height > 0 && (block.BorderLeft || block.BorderRight || block.BoxBorder || block.BorderTop || block.BorderBottom) {
			// One colour for every edge the block draws: an uncoloured border is
			// the reader's light hairline, a coloured one is what the sheet
			// asked for — a `border-left: 4px solid #c00` quote used to render a
			// grey bar, which is the one thing the author definitely decided.
			color := sdl.Color{R: 170, G: 178, B: 192, A: 255}
			if block.HasBorderColor {
				color = block.BorderColor
			}
			color = webFadeColor(color, d.Paper, block.Style)
			// A side bar defaults wider than a horizontal rule, because it is
			// the quote/sidebar idiom rather than a divider.
			barW := int32(math.Round(block.BorderWidth))
			if barW <= 0 {
				barW = 3
			}
			if barW > 12 {
				barW = 12
			}
			ruleH := barW
			if block.BorderWidth <= 0 {
				ruleH = 2
			}
			top := startGrid * rowH
			if block.BorderLeft {
				layout.Rects = append(layout.Rects, webRect{
					Rect:  sdl.Rect{X: padX + indent - 8, Y: top, W: barW, H: height},
					Color: color,
					Z:     block.ZIndex,
				})
			}
			if block.BorderRight {
				// Mirrors the left bar into the same gutter on the other side.
				x := padX + contentW - int32(block.Style.IndentRight) + 5
				layout.Rects = append(layout.Rects, webRect{
					Rect:  sdl.Rect{X: x, Y: top, W: barW, H: height},
					Color: color,
					Z:     block.ZIndex,
				})
			}
			if block.BoxBorder {
				x := padX + indent - 6
				w := contentW - indent + 12
				if x < padX {
					x = padX
					w = contentW
				}
				layout.Rects = append(layout.Rects, webRect{
					Rect:      sdl.Rect{X: x, Y: top, W: w, H: height},
					Color:     color,
					Stroke:    true,
					Thickness: ruleH,
					Z:         block.ZIndex,
					Radius:    radius,
				})
			} else {
				if block.BorderTop {
					layout.Rects = append(layout.Rects, webRect{
						Rect:  sdl.Rect{X: padX, Y: top, W: contentW, H: ruleH},
						Color: color,
						Z:     block.ZIndex,
					})
				}
				if block.BorderBottom {
					layout.Rects = append(layout.Rects, webRect{
						Rect:  sdl.Rect{X: padX, Y: top + height - ruleH, W: contentW, H: ruleH},
						Color: color,
						Z:     block.ZIndex,
					})
				}
			}
		}
	}

}

// webHrefFollowable reports whether an anchor target is worth a cursor stop:
// http, https or a relative path. In-page anchors and non-web schemes
// (javascript:, mailto:, data:, file:) are not pages the browser can open.
func webHrefFollowable(href string) bool {
	trimmed := strings.TrimSpace(href)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return false
	}
	if i := strings.IndexByte(trimmed, ':'); i > 0 {
		// A colon after a slash is part of a path, not a scheme.
		if !strings.ContainsAny(trimmed[:i], "/?#") {
			scheme := strings.ToLower(trimmed[:i])
			return scheme == "http" || scheme == "https"
		}
	}
	return true
}

// collectWebLinks turns the laid-out link runs into one hit per destination.
// A link that wraps across rows, or a nav entry repeated in the header and the
// footer, appears once; walking it with the D-pad would otherwise stop twice
// on the same place. In-page anchors have nothing to fetch and are skipped.
func collectWebLinks(layout *webLayout) []webLinkHit {
	if layout == nil {
		return nil
	}
	var links []webLinkHit
	seen := map[string]bool{}
	for rowIdx := 0; rowIdx < len(layout.Rows); rowIdx++ {
		row := layout.Rows[rowIdx]
		for i := 0; i < len(row.Runs); {
			run := row.Runs[i]
			if run.Font == nil || !webHrefFollowable(run.Href) {
				i++
				continue
			}
			href := run.Href
			startX := run.X
			endX := run.X
			label := ""
			j := i
			for ; j < len(row.Runs); j++ {
				cur := row.Runs[j]
				if cur.Href != href || cur.Font == nil {
					break
				}
				w, _, err := cur.Font.SizeUTF8(cur.Text)
				if err != nil {
					break
				}
				label += " " + cur.Text
				if end := cur.X + int32(w); end > endX {
					endX = end
				}
			}
			i = j
			if endX <= startX || seen[href] {
				continue
			}
			seen[href] = true
			links = append(links, webLinkHit{
				Row:  rowIdx,
				X:    startX,
				W:    endX - startX,
				Href: href,
				Text: strings.Join(strings.Fields(label), " "),
			})
		}
	}
	return links
}

func webLuminance(c sdl.Color) float64 {
	channel := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.03928 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(c.R) + 0.7152*channel(c.G) + 0.0722*channel(c.B)
}

func webContrast(a, b sdl.Color) float64 {
	la, lb := webLuminance(a), webLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// webReadableColor keeps author colours when they read on the page background
// and flips them when a page assumed its own background (a common reason a
// light page renders as invisible text).
func webReadableColor(fg, bg sdl.Color) sdl.Color {
	if webContrast(fg, bg) >= 2.4 {
		return fg
	}
	if webLuminance(bg) > 0.4 {
		return sdl.Color{R: 28, G: 32, B: 38, A: 255}
	}
	return sdl.Color{R: 242, G: 245, B: 250, A: 255}
}

func webResolveColor(style webStyle, doc *webDocument) sdl.Color {
	// An element with its own background (an inline <code> chip, a tinted
	// box) must be judged against that background, not the page: a light chip
	// on a dark page would otherwise draw light text on light fill.
	bg := doc.Paper
	if style.HasBg {
		bg = style.Background
	}
	color := doc.Text
	if style.HasColor {
		color = style.Color
	} else if style.Link {
		if webLuminance(bg) > 0.4 {
			color = sdl.Color{R: 20, G: 88, B: 200, A: 255}
		} else {
			color = sdl.Color{R: 130, G: 170, B: 255, A: 255}
		}
	}
	// Opacity is painted by blending toward the surface the text sits on: the
	// reader has no compositing layer, and a faded label should still read.
	return webFadeColor(webReadableColor(color, bg), bg, style)
}

// webRectCorner is the radius a rect is painted with: the author's
// border-radius clamped to half the shorter side (so a percentage or a huge
// value is a pill, not an error), or the reader's own crisp default.
func webRectCorner(bg webRect, fallback int32) int32 {
	radius := bg.Radius
	if radius <= 0 {
		radius = fallback
	}
	limit := bg.Rect.W
	if bg.Rect.H < limit {
		limit = bg.Rect.H
	}
	if limit > 0 && radius > limit/2 {
		radius = limit / 2
	}
	if radius < 0 {
		radius = 0
	}
	return radius
}

// webGradientCornerInset is how far a rounded corner cuts into the row at dy,
// the same circle approximation gradientRoundedRect uses for the app's own
// surfaces.
func webGradientCornerInset(radius, height, dy int32) int32 {
	if radius <= 0 || dy < 0 || dy >= height {
		return 0
	}
	if dy < radius {
		return radius - int32(math.Sqrt(float64(2*radius*dy-dy*dy)))
	}
	if d := dy - (height - radius); d > 0 {
		return radius - int32(math.Sqrt(float64(2*radius*d-d*d)))
	}
	return 0
}

// drawWebGradient paints a ramp inside rect, band by band. The band count is
// capped so a tall hero card costs a bounded number of fills per frame while a
// small header still gets one band per row: at 96 bands a 40-px header is
// per-pixel, and a 600-px hero is a 6-px staircase nobody can see.
func drawWebGradient(renderer *sdl.Renderer, rect sdl.Rect, radius int32, g webGradient) {
	if renderer == nil || rect.W <= 0 || rect.H <= 0 || len(g.Stops) == 0 {
		return
	}
	const maxBands = 96
	if g.Vertical {
		bands := rect.H
		if bands > maxBands {
			bands = maxBands
		}
		if bands < 1 {
			bands = 1
		}
		for i := int32(0); i < bands; i++ {
			y0 := rect.Y + i*rect.H/bands
			y1 := rect.Y + (i+1)*rect.H/bands
			if y1 <= y0 {
				continue
			}
			// The colour is sampled at the band's middle, so a coarse band does
			// not look like the old flat fill with a seam in it.
			color := g.At((float64(i) + 0.5) / float64(bands))
			x0, w := rect.X, rect.W
			if radius > 0 {
				// The inset is sampled at the band's middle, so a rounded corner
				// steps once per band instead of once per scanline.
				inset := webGradientCornerInset(radius, rect.H, (y0+y1)/2-rect.Y)
				x0 += inset
				w -= inset * 2
			}
			if w <= 0 {
				continue
			}
			renderer.SetDrawColor(color.R, color.G, color.B, color.A)
			renderer.FillRect(&sdl.Rect{X: x0, Y: y0, W: w, H: y1 - y0})
		}
		return
	}

	bands := rect.W
	if bands > maxBands {
		bands = maxBands
	}
	if bands < 1 {
		bands = 1
	}
	for i := int32(0); i < bands; i++ {
		x0 := rect.X + i*rect.W/bands
		x1 := rect.X + (i+1)*rect.W/bands
		if x1 <= x0 {
			continue
		}
		color := g.At((float64(i) + 0.5) / float64(bands))
		y0, h := rect.Y, rect.H
		if radius > 0 {
			inset := webGradientCornerInset(radius, rect.W, (x0+x1)/2-rect.X)
			y0 += inset
			h -= inset * 2
		}
		if h <= 0 {
			continue
		}
		renderer.SetDrawColor(color.R, color.G, color.B, color.A)
		renderer.FillRect(&sdl.Rect{X: x0, Y: y0, W: x1 - x0, H: h})
	}
}

// drawWebDocument paints the styled page inside rect starting at startRow.
func drawWebDocument(renderer *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, startRow int) {
	if doc == nil || layout == nil || layout.RowH <= 0 || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if startRow < 0 {
		startRow = 0
	}
	if startRow >= len(layout.Rows) {
		startRow = 0
	}
	renderWithClip(renderer, rect.X, rect.Y, rect.W, rect.H, func(r *sdl.Renderer) {
		if doc.HasPaperGradient {
			// The page background is a ramp too, not the average of one: a
			// body gradient is most of what a landing page looks like.
			drawWebGradient(r, rect, 0, doc.PaperGradient)
		} else {
			r.SetDrawColor(doc.Paper.R, doc.Paper.G, doc.Paper.B, 255)
			r.FillRect(&sdl.Rect{X: rect.X, Y: rect.Y, W: rect.W, H: rect.H})
		}

		offset := int32(startRow) * layout.RowH
		if len(layout.Floats) > 0 {
			drawWebFloats(r, config, doc, layout, rect, offset, true)
		}
		for _, bg := range layout.Rects {
			y := rect.Y + bg.Rect.Y - offset
			if y+bg.Rect.H <= rect.Y || y >= rect.Y+rect.H {
				continue
			}
			if bg.Stroke {
				thickness := bg.Thickness
				if thickness < 1 {
					thickness = 1
				}
				strokeRoundedRect(r, rect.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 3), thickness, bg.Color)
				continue
			}
			if bg.HasGradient {
				drawWebGradient(r, sdl.Rect{X: rect.X + bg.Rect.X, Y: y, W: bg.Rect.W, H: bg.Rect.H}, webRectCorner(bg, 2), bg.Gradient)
				continue
			}
			fillRoundedRect(r, rect.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 2), bg.Color)
		}
		// Images draw on their own reserved rows, before the text, so a page
		// with a header image scrolls the photo away like a normal line.
		for i := range layout.Images {
			im := layout.Images[i]
			y := rect.Y + im.Rect.Y - offset
			if im.Rect.H <= 0 || y >= rect.Y+rect.H || y+im.Rect.H <= rect.Y {
				continue
			}
			drawWebImage(r, config, doc, im, rect.X+im.Rect.X, y)
		}
		// Video play cards come after the images on the same terms: they
		// own their reserved rows and draw before the text rows.
		for i := range layout.Videos {
			v := layout.Videos[i]
			y := rect.Y + v.Rect.Y - offset
			if v.Rect.H <= 0 || y >= rect.Y+rect.H || y+v.Rect.H <= rect.Y {
				continue
			}
			drawWebVideo(r, config, doc, v, rect.X+v.Rect.X, y)
		}
		for i := startRow; i < len(layout.Rows); i++ {
			row := layout.Rows[i]
			y := rect.Y + row.Y - offset
			if y >= rect.Y+rect.H {
				break
			}
			// Inline backgrounds first, merged across adjacent words of the
			// same style so a code phrase is one chip instead of one box per
			// word.
			for i := 0; i < len(row.Runs); i++ {
				run := row.Runs[i]
				if !run.HasBg || run.Font == nil {
					continue
				}
				w, h, err := run.Font.SizeUTF8(run.Text)
				if err != nil {
					continue
				}
				startX := rect.X + run.X - 2
				endX := rect.X + run.X + int32(w)
				for j := i + 1; j < len(row.Runs); j++ {
					next := row.Runs[j]
					if !next.HasBg || next.Bg != run.Bg || next.Font == nil {
						break
					}
					nw, _, e := next.Font.SizeUTF8(next.Text)
					if e != nil {
						break
					}
					endX = rect.X + next.X + int32(nw)
					i = j
				}
				fillRoundedRect(r, startX, y, endX-startX+4, int32(h), 3, run.Bg)
			}
			for _, run := range row.Runs {
				drawTextRun(r, config, run.Font, run.Text, run.Color, rect.X+run.X, y)
				if run.Bold {
					// One pixel of fake weight: the app ships one font face, and
					// a second offset copy reads as bold at this size.
					drawTextRun(r, config, run.Font, run.Text, run.Color, rect.X+run.X+1, y)
				}
				if (run.Underline || run.Strike) && run.Font != nil {
					if w, _, err := run.Font.SizeUTF8(run.Text); err == nil {
						r.SetDrawColor(run.Color.R, run.Color.G, run.Color.B, 220)
						if run.Underline {
							r.FillRect(&sdl.Rect{
								X: rect.X + run.X,
								Y: y + int32(run.Font.Height()) - 5,
								W: int32(w),
								H: 1,
							})
						}
						if run.Strike {
							r.FillRect(&sdl.Rect{
								X: rect.X + run.X,
								Y: y + int32(run.Font.Height())/2,
								W: int32(w),
								H: 1,
							})
						}
					}
				}
			}
		}
		if len(layout.Floats) > 0 {
			drawWebFloats(r, config, doc, layout, rect, offset, false)
		}
	})
}

// webDocumentRows returns the row count for a styled page, or 0 when the page
// has no styled document (so callers can fall back to plain text).
func webDocumentRows(doc *webDocument, config *Config, elem Element, width int32) (*webLayout, int) {
	if doc == nil {
		return nil, 0
	}
	layout := doc.layoutFor(config, elem, width)
	if layout == nil || len(layout.Rows) == 0 {
		return nil, 0
	}
	return layout, len(layout.Rows)
}
