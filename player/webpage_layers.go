package main

// webpage_layers.go — the reader's layering engine: which boxes leave the
// flow, how they stack, and the order a page is painted in.
//
// A browser paints a page as a stack (CSS 2.1 Appendix E): the canvas, then
// positioned boxes with a negative z-index, then the backgrounds and borders
// of the flow in tree order, then floats, then the flow's text and pictures,
// then positioned boxes and sticky bars in z-index order — each positioned
// subtree as one unit, inside the stacking context it belongs to. The reader
// follows that model:
//
//   - An absolutely positioned block is one layer together with everything
//     inside it (webLayerInfo). Its heading, paragraphs and buttons used to
//     stay in the flow while only its own text moved, and the box painted
//     around both halves spanned half the page. The subtree is measured at
//     the layer's width (shrink-to-fit when it has none), painted with its own
//     surfaces and placed at its insets in the nearest positioned ancestor —
//     or, without insets, at its static position: where it would have started
//     in the flow, which a marker block records (layerMarker). Positioned
//     elements inside a layer stay in that layer's flow.
//   - A position: fixed block is a layer pinned to the screen (webFloat.Pin):
//     a site's header bar, bottom navigation or chat button stays where the
//     page put it in the view while the page scrolls under it, and its links
//     are tapped where they are drawn. Only a box too big to pin (taller than
//     the view, or most of it across the whole width) scrolls with the page,
//     and a box covering the whole screen (a modal backdrop, a cookie wall)
//     keeps its content in the flow, where the reader can still read past it.
//   - Every positioned element has a box (webBoxInfo.anchor when it has no
//     surface of its own), so it can be a containing block; a positioned
//     container whose children all left the flow keeps a line of its own
//     (layerSpacer) so it still has a place and a height.
//   - An absolutely positioned inline element (a badge, a ribbon, a label over
//     a photo) leaves its line and is a layer too (detachInlineLayer); the
//     line around it goes on as if it were not there.
//   - Layers are ordered by a stacking key: the stacking contexts around them
//     (a positioned ancestor with a z-index, a fixed or sticky one, a
//     translucent or transformed one, one isolated by isolation, a filter, a
//     blend mode, will-change, contain, a clip path or a mask, a flex item
//     with a z-index), then their own z-index, then document order. A
//     z-index: 9999 badge inside a z-index: 1 card no longer covers a
//     z-index: 2 header.
//   - A relatively positioned element of the flow that comes after a layer
//     in that order and overlaps it is painted over it (webLiftPositioned):
//     a hero's `relative z-10` heading over its `absolute inset-0` picture.
//   - Surfaces in the flow paint in tree order (webSortPaintOrder): a
//     section's background goes under the code block inside it, not over it.
//   - CSS floats paint under positioned content, and sticky bars and
//     positioned layers share one z-ordered pass. A sticky element is pinned
//     with everything inside it (a header of logo, menu and buttons, a table's
//     header cells, its positioned and floated children), copied from the
//     laid-out page by box membership (webCollectStickies); it leaves with its
//     containing block, and a bottom-sticky bar sticks to the bottom edge.
//   - Screen-reader-only and off-screen positioned blocks are not painted over
//     the page, and an overflow: hidden containing block — or a box around it
//     — clips its layers (carousels, rounded cards).
//   - Links and form controls inside layers and floats are cursor stops, and a
//     tap goes to the topmost one.

import (
	"math"
	"sort"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// Where a layer goes in the paint order (webFloat.Phase).
const (
	// webLayerAbove is a positioned layer over the flow (z-index 0 or more).
	webLayerAbove uint8 = iota
	// webLayerUnder has a negative z-index in the page's own stacking
	// context: it is painted before the flow's backgrounds.
	webLayerUnder
	// webLayerUnderText has a negative z-index inside a stacking context:
	// over the flow's backgrounds, under its text.
	webLayerUnderText
	// webLayerFloat is a CSS float: over the flow's backgrounds and borders,
	// under its text, its pictures and every positioned layer.
	webLayerFloat
)

// How a position: fixed layer holds to the screen (webFloat.Pin).
const (
	// webPinNone: the layer scrolls with the page.
	webPinNone uint8 = iota
	// webPinTop: Y below the view's top edge.
	webPinTop
	// webPinBottom: Bottom above the view's bottom edge.
	webPinBottom
	// webPinCenter: centred in the view's height (inset: 0; margin: auto).
	webPinCenter
)

const (
	// webMaxLayers bounds the positioned subtrees one page opens. Past it a
	// positioned element's own text is a layer and its children stay in the
	// flow, as they always did.
	webMaxLayers = 400
	// webMaxStickyShare caps a pinned sticky box that spans most of the width
	// at two fifths of the page view: a taller one (a whole column) would
	// cover the page it heads, so it scrolls with the page instead. A narrow
	// one (a side column beside the text) may be taller.
	webMaxStickyShare = 0.4
	// webMaxStickies bounds the sticky elements one page records.
	webMaxStickies = 200
	// webMaxPositioned bounds the relatively positioned elements one page
	// records for lifting (webLiftPositioned); past it they paint with the
	// flow, as they used to.
	webMaxPositioned = 2000
)

// webStaticPos is a layer's static position in page coordinates: where its
// root would have started in the flow, and that flow column's left edge.
type webStaticPos struct {
	X, Y int32
}

// webStickyInfo is one position: sticky element as the parser saw it: its
// style (insets, z-index), its own box and the last box opened inside it
// (its subtree is every box between), the box it may not leave, and its
// place in the document.
type webStickyInfo struct {
	style    webStyle
	rootBox  int
	lastBox  int
	limitBox int
	seq      int
}

// webPositionedInfo is one relatively positioned element of the flow: its
// box, the last box opened inside it (its subtree is every box between) and
// its stacking key. CSS paints such an element, everything in it
// included, in the positioned pass in stacking order — over an earlier layer
// it overlaps — which is where layout lifts it (webLiftPositioned).
type webPositionedInfo struct {
	rootBox int
	lastBox int
	key     webStackKey
}

// webSuspendedLine is the paragraph an absolutely positioned inline element
// (a badge, a ribbon) interrupted: the element is laid out as a layer of its
// own and the line around it goes on after it as if it were not there
// (webParser.detachInlineLayer). The pending inline edges and list marker are
// the line's, not the layer's.
type webSuspendedLine struct {
	current          *webBlock
	space            bool
	inlineGroup      int
	pendingLead      float64
	pendingChipStart float64
	pendingChip      bool
	pendingOpens     bool
	pendingB         string
	pendingBStyle    webStyle
	hasPendingBStyle bool
	flushes          int
}

// webStackLevel is one participant in a stacking order: a z-index and a
// place in the document (the element's parser sequence).
type webStackLevel struct {
	z     int
	order int
}

// webStackKey orders layers the way nested stacking contexts do. Level 0 is
// what competes at page level: the outermost stacking context around the
// layer, or the layer itself when there is none. A deeper level only orders
// layers inside the same context. The outermost and the innermost context are
// kept, then the layer itself.
type webStackKey struct {
	levels [3]webStackLevel
	n      int
}

// webFreeStackKey is the key of a box outside every stacking context.
func webFreeStackKey(z, order int) webStackKey {
	k := webStackKey{n: 1}
	k.levels[0] = webStackLevel{z: z, order: order}
	return k
}

// less reports whether a is painted before (under) b.
func (a webStackKey) less(b webStackKey) bool {
	for i := 0; i < a.n && i < b.n; i++ {
		if a.levels[i].z != b.levels[i].z {
			return a.levels[i].z < b.levels[i].z
		}
		if a.levels[i].order != b.levels[i].order {
			return a.levels[i].order < b.levels[i].order
		}
	}
	return a.n < b.n
}

// webLayerPhaseFor picks a positioned layer's paint pass from its key and its
// own z-index.
func webLayerPhaseFor(key webStackKey, z int) uint8 {
	switch {
	case key.n > 0 && key.levels[0].z < 0:
		return webLayerUnder
	case z < 0:
		return webLayerUnderText
	}
	return webLayerAbove
}

// webLayerInfo is what the parser knows about one positioned subtree when
// its root opens: the root's style (insets, width, z-index, translate), how
// many ancestor boxes and flex levels lie outside it, the root's own box and
// its stacking key. Blocks inside carry the layer's number (webBlock.Layer).
type webLayerInfo struct {
	style      webStyle
	outerBoxes int
	outerFlex  int
	rootBox    int
	key        webStackKey
	// flexGroup is the flex or grid container the root itself opens (0 when
	// it is a plain block): a layer stretched between its top and bottom
	// insets hands that container its height (layoutLayer).
	flexGroup int
	// parent is the layer this one sits in (0 for one in the page's flow): a
	// close button at the corner of a positioned dialog, a dropdown under a
	// fixed header's menu. It is laid out in its parent's pass, against the
	// boxes that layer measured, and painted with it.
	parent int
	depth  int
}

// webLayerGroup is the blocks of one layer, in document order. layer is 0 for
// a lone positioned block that opened no layer (see webMaxLayers).
type webLayerGroup struct {
	layer  int
	blocks []webBlock
	// nested are the layers inside this one (webLayerInfo.parent).
	nested []webLayerGroup
}

// webMaxLayerDepth bounds how deep positioned boxes nest as layers of their
// own; deeper ones are laid out in their parent's flow, as every nested one
// used to be.
const webMaxLayerDepth = 4

// --- Parser ------------------------------------------------------------------

// webStyleIsAnchor reports a positioned element: the containing block of the
// absolutely positioned boxes inside it. Such an element always gets a box,
// so layout can find its rectangle.
func webStyleIsAnchor(st *webStyle) bool {
	return st.Position != webPositionStatic || st.Sticky
}

// webStackingContext reports an element whose box starts a stacking context
// the reader can tell: a positioned box with an integer z-index, any fixed or
// sticky box, a transformed one, one isolated by isolation, a filter, a blend
// mode, will-change, contain, a clip path or a mask (webStyle.Isolate), and a
// translucent one. Opacity is inherited here as the fade a group applies, so
// only the element that set it (its parent's differs) starts the context;
// parent may be nil.
func webStackingContext(st, parent *webStyle) bool {
	switch {
	case st.Position == webPositionFixed || st.Sticky:
		return true
	case st.ZSet && st.Position != webPositionStatic:
		return true
	case st.HasTranslate || st.Isolate:
		return true
	}
	if !st.HasOpacity || st.Opacity >= 0.999 {
		return false
	}
	return parent == nil || !parent.HasOpacity || parent.Opacity != st.Opacity
}

// webLayerBlockLevel mirrors startTag's choice of a block push.
func webLayerBlockLevel(name string, st *webStyle) bool {
	if st.DisplayContents {
		return false
	}
	switch name {
	case "ul", "ol":
		return true
	case "li":
		return !st.DisplayInline
	}
	return (webBlockElement(name) && !st.DisplayInline) || st.DisplayBlock || st.Display >= webDisplayTable
}

// webLayerHiddenBlock reports a positioned block a browser shows nothing of:
// fully transparent, or the visually-hidden idiom — a 1×1 px box (sr-only,
// visually-hidden, screen-reader-text) whose text is for screen readers only.
// The reader used to paint such a heading at its containing block's corner,
// over the page. An inline one stays in its line: it is often the only label
// an icon link has.
func webLayerHiddenBlock(name string, st *webStyle) bool {
	if st.Position != webPositionAbsolute && st.Position != webPositionFixed {
		return false
	}
	if !webLayerBlockLevel(name, st) {
		return false
	}
	if st.HasOpacity && st.Opacity <= 0.01 {
		return true
	}
	if webOffCanvas(st) {
		return true
	}
	// 1×1 px, or 1 px in one direction with overflow hidden (older
	// `.element-invisible` rules set only the height).
	tiny := func(px float64) bool { return px > 0 && px <= 2 }
	clipped := st.OverflowX != 0 || st.OverflowY != 0
	return (tiny(st.Width) && tiny(st.Height)) || (clipped && (tiny(st.Width) || tiny(st.Height)))
}

// webOffCanvas reports a positioned box parked wholly off the screen until a
// script slides it in: a fixed one past the left or top edge — html5test's
// index menu is `position: fixed; left: -240px; width: 220px` with 10px of
// padding — or any one thousands of pixels out (a skip link at -9999px). A
// browser shows nothing of it. Nested inside another positioned box the
// reader lays it out in that box's flow, where its inset is lost, and the
// menu covered a quarter of the page. A translate may bring it back, so a
// box with one is left to layout.
func webOffCanvas(st *webStyle) bool {
	if st.HasTranslate {
		return false
	}
	left := st.HasLeft && st.InsetPct[3] == 0
	top := st.HasTop && st.InsetPct[0] == 0
	if (left && st.Left <= -999) || (top && st.Top <= -999) {
		return true
	}
	if st.Position != webPositionFixed {
		// An absolute box hangs off its containing block, which is rarely
		// at the screen's edge: a badge at left: -30px stays in view.
		return false
	}
	// A drawer slid out by a percentage of its own width: `left: -100%;
	// width: 80%`.
	if st.HasLeft && st.InsetPct[3] < 0 && st.WidthPct > 0 && st.InsetPct[3]+st.WidthPct <= 0 {
		return true
	}
	w, h := st.Width, st.Height
	if st.BoxSizing != 1 {
		w += st.PadLeft + st.PadRight
		h += st.PadTop + st.PadBot
	}
	right := st.HasRight && st.InsetPct[1] == 0 && !st.HasLeft
	return (left && st.Width > 0 && st.Left < 0 && st.Left+w <= 0) ||
		(right && st.Width > 0 && st.Right < 0 && st.Right+w <= 0) ||
		(top && st.Height > 0 && st.Top < 0 && st.Top+h <= 0)
}

// webLayerCoversScreen reports a box that would cover most of the screen: a
// modal backdrop, a cookie wall, an app shell (inset: 0, or most of the
// viewport in both directions).
func webLayerCoversScreen(st *webStyle) bool {
	vw, vh := webMediaViewport()
	tall := (st.HasTop && st.HasBottom) || (vh > 0 && math.Max(st.Height, st.MinHeight) >= vh*0.5)
	wide := (st.HasLeft && st.HasRight) || st.WidthPct >= 80 || (vw > 0 && st.Width >= vw*0.8)
	return tall && wide
}

// wantsLayer decides whether an absolutely or fixed positioned block opens a
// layer for its whole subtree. It does, unless the box covers the screen (a
// modal backdrop, a cookie wall, an app shell: inset: 0 or most of the
// viewport both ways) and is placed against the screen or the page itself —
// a fixed one, or an absolute one with no positioned ancestor (a positioned
// <html> or <body>, a common reset, is the page for this). Such a box has no
// close button the reader can press, so its content stays in the flow where
// it can be read past. Inside a positioned ancestor a covering box is that
// ancestor's overlay and is a layer like any other.
func (p *webParser) wantsLayer(style *webStyle) bool {
	if style.Position != webPositionAbsolute && style.Position != webPositionFixed {
		return false
	}
	if style.Position == webPositionAbsolute {
		for i := range p.stack {
			f := &p.stack[i]
			if f.el.tag == "html" || f.el.tag == "body" {
				continue
			}
			if (f.kind == "block" || f.kind == "list") && webStyleIsAnchor(&f.style) {
				return true
			}
		}
	}
	return !webLayerCoversScreen(style)
}

// openLayerIn returns the layer the first n open elements put new blocks in:
// the innermost positioned subtree among them, or 0 for the flow.
func (p *webParser) openLayerIn(n int) int {
	for i := min(n, len(p.stack)) - 1; i >= 0; i-- {
		if p.stack[i].layer != 0 {
			return p.stack[i].layer
		}
	}
	return 0
}

// layerDepthIn is how many layers nest at the first n open elements.
func (p *webParser) layerDepthIn(n int) int {
	if layer := p.openLayerIn(n); layer > 0 && layer <= len(p.doc.Layers) {
		return p.doc.Layers[layer-1].depth
	}
	return 0
}

// blockFrameSeq is the parser sequence of the element a new block takes its
// style from (nearestBlockStyle's element): its place in document order.
func (p *webParser) blockFrameSeq() int {
	for i := len(p.stack) - 1; i >= 0; i-- {
		if p.stack[i].kind == "block" {
			return p.stack[i].seq
		}
	}
	return 0
}

// newLayer records a positioned subtree whose root is about to be pushed with
// box boxID and sequence seq. The root is not on the stack yet, so the stack
// is exactly its ancestors.
func (p *webParser) newLayer(style webStyle, boxID, seq int) int {
	info := webLayerInfo{style: style, outerBoxes: len(p.boxPath()), outerFlex: len(p.flexStack), rootBox: boxID}
	info.key = p.stackKey(style, seq)
	info.parent = p.openLayerIn(len(p.stack))
	info.depth = p.layerDepthIn(len(p.stack)) + 1
	p.doc.Layers = append(p.doc.Layers, info)
	return len(p.doc.Layers)
}

// stackKey is the stacking key of an element about to be pushed with
// sequence seq (the stack is exactly its ancestors): the stacking contexts
// around it, the outermost and the innermost kept, then its own z-index and
// place in the document. A flex or grid item with a z-index is a stacking
// context too, positioned or not, as in CSS.
func (p *webParser) stackKey(style webStyle, seq int) webStackKey {
	var ctx []webStackLevel
	for i := range p.stack {
		f := &p.stack[i]
		var parent *webStyle
		if i > 0 {
			parent = &p.stack[i-1].style
		}
		if f.kind != "skip" && (webStackingContext(&f.style, parent) || (f.flexItem && f.style.ZSet)) {
			ctx = append(ctx, webStackLevel{z: f.style.ZIndex, order: f.seq})
		}
	}
	if len(ctx) > 2 {
		ctx = []webStackLevel{ctx[0], ctx[len(ctx)-1]}
	}
	var key webStackKey
	for _, level := range ctx {
		key.levels[key.n] = level
		key.n++
	}
	key.levels[key.n] = webStackLevel{z: style.ZIndex, order: seq}
	key.n++
	return key
}

// detachesInlineLayer reports an absolutely or fixed positioned inline element
// that CSS takes out of its line (it is blockified, CSS 2.1 §9.7): a "Sale"
// badge on a product card, a ribbon, a duration over a video thumbnail, a
// label over a photo. The reader kept such a span in the text, so the badge
// read as a word before the card. It becomes a layer at its insets in its
// containing block, unless the reader could not place it there or would lose
// something by moving it:
//
//   - a link, a label or a form control keeps its place (a stretched card link
//     is the card's way to its page; its text is often all the link has);
//   - a visually hidden one (1–2 px: sr-only, visually-hidden; or parked
//     far off the page) keeps its place too — it is often the only name an
//     icon link has;
//   - one whose containing block is a positioned inline element (an icon link
//     with position: relative) has no box the reader measures, and would land
//     at some outer box's corner, so it stays in its line;
//   - inside a layer, a form control or a button it stays where it is, as
//     positioned elements inside a layer do.
func (p *webParser) detachesInlineLayer(name string, st *webStyle) bool {
	if st.Position != webPositionAbsolute && st.Position != webPositionFixed {
		return false
	}
	switch name {
	case "a", "label", "button", "input", "select", "textarea", "option", "img", "picture", "svg",
		"video", "audio", "iframe", "object", "embed", "canvas", "br", "wbr", "area", "map":
		return false
	}
	if tiny := func(px float64) bool { return px > 0 && px <= 2 }; tiny(st.Width) || tiny(st.Height) {
		return false
	}
	// The older visually-hidden idiom parks the text far off the page
	// (left: -9999px); it stays a label in its line as well.
	if (st.HasLeft && st.Left <= -500) || (st.HasTop && st.Top <= -500) || (st.HasRight && st.Right <= -500) {
		return false
	}
	if st.HasOpacity && st.Opacity <= 0.01 {
		return false
	}
	if p.buttonCtrl != nil || p.textarea != nil || p.selectCtrl != nil || p.inHead ||
		len(p.doc.Layers) >= webMaxLayers || p.openLayerIn(len(p.stack)) != 0 || !p.wantsLayer(st) {
		return false
	}
	if st.Position == webPositionAbsolute {
		for i := len(p.stack) - 1; i >= 0; i-- {
			f := &p.stack[i]
			if f.kind == "skip" || f.kind == "contents" || !webStyleIsAnchor(&f.style) {
				continue
			}
			if f.kind != "block" && f.kind != "list" {
				return false
			}
			break
		}
	}
	return true
}

// detachInlineLayer opens a detachesInlineLayer element as a layer without
// ending the line it sits in: the paragraph is set aside, the element's own
// blocks go to its layer, and the paragraph resumes when the element closes
// (resumeLine), so "Only 3 left" around a badge stays one line.
func (p *webParser) detachInlineLayer(el webElement, style webStyle, attrs string, selfClose bool, name string) {
	s := &webSuspendedLine{
		current: p.current, space: p.space, inlineGroup: p.inlineGroup,
		pendingLead: p.pendingLead, pendingChipStart: p.pendingChipStart,
		pendingChip: p.pendingChip, pendingOpens: p.pendingOpens,
		pendingB: p.pendingB, pendingBStyle: p.pendingBStyle, hasPendingBStyle: p.hasPendingBStyle,
		flushes: p.lineFlushes,
	}
	p.current, p.space, p.inlineGroup = nil, false, 0
	p.clearInlineLead()
	p.pendingB, p.pendingBStyle, p.hasPendingBStyle = "", webStyle{}, false
	p.push(el, style, "block", attrs)
	p.stack[len(p.stack)-1].suspended = s
	if selfClose {
		p.endTag(name)
	}
}

// resumeDetachedAbove finishes the detached layers open above stack index i
// (unclosed inside an element that is closing, or at the end of the page):
// each one's last block goes to its layer and its line resumes.
func (p *webParser) resumeDetachedAbove(i int) {
	for k := len(p.stack) - 1; k > i && k >= 0; k-- {
		if s := p.stack[k].suspended; s != nil {
			p.flush()
			p.resumeLine(s)
			p.stack[k].suspended = nil
		}
	}
}

// resumeLine puts back the paragraph a detached layer set aside. The layer's
// own last block has been flushed already.
func (p *webParser) resumeLine(s *webSuspendedLine) {
	if s == nil {
		return
	}
	p.current, p.space, p.inlineGroup = s.current, s.space, s.inlineGroup
	p.pendingLead, p.pendingChipStart = s.pendingLead, s.pendingChipStart
	p.pendingChip, p.pendingOpens = s.pendingChip, s.pendingOpens
	p.pendingB, p.pendingBStyle, p.hasPendingBStyle = s.pendingB, s.pendingBStyle, s.hasPendingBStyle
	p.lineFlushes = s.flushes
}

// webStackingProperty reports a declaration that makes its element a stacking
// context although the reader draws none of its effect (webStyle.Isolate):
// isolation: isolate (Tailwind's `isolate`), a filter or backdrop filter, a
// blend mode, will-change naming such a property, contain: paint/layout,
// perspective, a clip path or a mask. A z-index inside such an element orders
// only what is inside it: a `-z-10` picture in an isolated hero stays over
// the hero's own background instead of going under the page.
func webStackingProperty(name, value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "", "none", "auto", "normal", "initial", "unset", "inherit", "revert", "revert-layer":
		return false
	}
	switch name {
	case "isolation":
		return v == "isolate"
	case "will-change":
		for _, part := range strings.Split(v, ",") {
			switch strings.TrimSpace(part) {
			case "transform", "opacity", "filter", "backdrop-filter", "perspective", "isolation",
				"mix-blend-mode", "clip-path", "mask", "contain", "translate", "rotate", "scale":
				return true
			}
		}
		return false
	case "contain":
		for _, part := range webCSSSpaceFields(v) {
			switch part {
			case "layout", "paint", "strict", "content":
				return true
			}
		}
		return false
	}
	return true
}

// layerMarker leaves an empty block in the flow where a layer's root is about
// to open: layout records its place as the layer's static position, which a
// box without insets keeps (CSS's "static position"), so a dropdown or a
// caption without top/left sits where its markup is instead of jumping to its
// containing block's corner. A direct flex or grid item gets none: the
// container drops an empty item, and the layer falls back to that corner.
func (p *webParser) layerMarker(layer, seq int) {
	if len(p.blocks) >= webMaxBlocks {
		return
	}
	st := p.topStyle()
	if st.TableRole != webTableRoleNone {
		// Inside a table an empty block could read as a cell of its own.
		return
	}
	webResetElementStyle(&st)
	st.Invisible = true
	st.Href, st.Control = "", nil
	marker := webBlock{Style: st, LayerMarker: layer, TreeSeq: seq}
	// A layer inside another keeps its static place in that layer's flow.
	marker.Layer = p.openLayerIn(len(p.stack))
	marker.Float = marker.Layer != 0
	marker.ContainerIDs = p.containerPath()
	marker.Boxes = p.boxPath()
	p.tagFlexBlock(&marker, st)
	marker.Details, marker.DetailsShown = p.detailsTag()
	// Inside a floated subtree the place is in the float (webpage_floats.go).
	marker.FloatGroup = p.openFloatIn(len(p.stack))
	p.blocks = append(p.blocks, marker)
}

// newStickyRoot records a position: sticky element about to be pushed with
// box boxID: its subtree is pinned as one bar once laid out. The box it may
// not leave is its nearest enclosing box, or its table for a table cell (none
// found: not recorded, or a header row would stay pinned over whatever
// follows the table).
func (p *webParser) newStickyRoot(style webStyle, boxID, seq int) int {
	if len(p.doc.Stickies) >= webMaxStickies || p.openLayerIn(len(p.stack)) != 0 {
		return 0
	}
	limit := 0
	for i := len(p.stack) - 1; i >= 0; i-- {
		f := &p.stack[i]
		if f.boxID == 0 {
			continue
		}
		if style.TableRole == webTableRoleCell && f.style.TableRole != webTableRoleTable {
			continue
		}
		limit = f.boxID
		break
	}
	if style.TableRole == webTableRoleCell && limit == 0 {
		return 0
	}
	p.doc.Stickies = append(p.doc.Stickies, webStickyInfo{style: style, rootBox: boxID, limitBox: limit, seq: seq})
	return len(p.doc.Stickies)
}

// layerSpacer gives a positioned container whose every child left the flow a
// line of its own. CSS still sizes it (its height and min-height) and it is
// the containing block its layers sit in; without a block it took no rows,
// had no rectangle, and its layers fell back to the page's corner. The line is
// one invisible character: it reserves the row and measures the box, and it
// paints nothing — the box's own surface comes from the container pass.
func (p *webParser) layerSpacer(i int) {
	if i < 0 || i >= len(p.stack) || len(p.blocks) >= webMaxBlocks {
		return
	}
	frame := &p.stack[i]
	if (frame.style.Position != webPositionRelative && !frame.style.Sticky) || p.openLayerIn(i+1) != 0 {
		return
	}
	if frame.blockStart < 0 || frame.blockStart >= len(p.blocks) {
		return
	}
	layers := 0
	for k := frame.blockStart; k < len(p.blocks); k++ {
		b := &p.blocks[k]
		switch {
		case b.Layer > 0 && b.Layer <= len(p.doc.Layers):
			// A fixed layer holds to the screen, not to this container.
			if p.doc.Layers[b.Layer-1].style.Position != webPositionFixed {
				layers++
			}
		case b.Layer == 0 && b.LayerMarker == 0:
			return
		}
	}
	if layers == 0 {
		return
	}
	st := frame.style
	webResetElementStyle(&st)
	st.Invisible = true
	st.Href, st.Control = "", nil
	spacer := webBlock{Style: st, Runs: []webRun{{Text: ".", Style: st}}, TreeSeq: frame.seq}
	for k := 0; k <= i; k++ {
		if id := p.stack[k].containerID; id != 0 {
			spacer.ContainerIDs = append(spacer.ContainerIDs, id)
		}
		if id := p.stack[k].boxID; id != 0 {
			spacer.Boxes = append(spacer.Boxes, id)
		}
	}
	if n := len(spacer.Boxes); n > 0 {
		spacer.BoxOwner = spacer.Boxes[n-1]
	}
	p.tagFlexBlock(&spacer, st)
	spacer.Details, spacer.DetailsShown = p.detailsTag()
	spacer.FloatGroup = p.openFloatIn(i + 1)
	p.blocks = append(p.blocks, spacer)
}

// --- Layout ------------------------------------------------------------------

// layoutLayers lays out every positioned subtree and parks it in the layer
// list, then puts all layers (CSS floats included) in paint order. Layers
// take no room in the flow; the page only grows when one reaches past its
// last line.
func (l *webLayouter) layoutLayers(blocks []webBlock, padX, contentW int32) {
	if l == nil || l.layout == nil {
		return
	}
	if len(blocks) > 0 && l.rowH > 0 {
		pageH := int32(len(l.layout.Rows)) * l.rowH
		var groups []webLayerGroup
		index := make(map[int]int, 8)
		for _, b := range blocks {
			if b.Layer == 0 {
				groups = append(groups, webLayerGroup{blocks: []webBlock{b}})
				continue
			}
			if gi, ok := index[b.Layer]; ok {
				groups[gi].blocks = append(groups[gi].blocks, b)
				continue
			}
			index[b.Layer] = len(groups)
			groups = append(groups, webLayerGroup{layer: b.Layer, blocks: []webBlock{b}})
		}
		bottom, parked := int32(0), pageH
		for _, g := range l.nestLayerGroups(groups, index) {
			bottom = max(bottom, l.layoutLayer(g, padX, contentW, pageH, &parked))
		}
		// The page grows by two screens at most: a dropdown or caption that
		// hangs past the last line stays reachable, a decoration parked far
		// below does not add a blank tail.
		if _, vh := webMediaViewport(); vh > 0 {
			bottom = min(bottom, pageH+int32(2*vh*l.baseSize/16))
		}
		for int32(len(l.layout.Rows))*l.rowH < bottom && len(l.layout.Rows) < webMaxRows {
			idx := int32(len(l.layout.Rows))
			l.layout.Rows = append(l.layout.Rows, webRow{Y: idx * l.rowH})
		}
		l.layout.Height = int32(len(l.layout.Rows)) * l.rowH
	}
	sort.SliceStable(l.layout.Floats, func(i, j int) bool {
		a, b := &l.layout.Floats[i], &l.layout.Floats[j]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		return a.Key.less(b.Key)
	})
}

// nestLayerGroups puts each layer's group inside the group of the layer it
// sits in (webLayerInfo.parent), so it is laid out in that layer's pass; a
// group whose parent laid nothing out is placed on the page like any other.
// index maps a layer to its group.
func (l *webLayouter) nestLayerGroups(groups []webLayerGroup, index map[int]int) []webLayerGroup {
	if l.doc == nil {
		return groups
	}
	children := make(map[int][]int)
	roots := make([]int, 0, len(groups))
	for i := range groups {
		parent := 0
		if layer := groups[i].layer; layer > 0 && layer <= len(l.doc.Layers) {
			parent = l.doc.Layers[layer-1].parent
		}
		if pi, ok := index[parent]; parent != 0 && ok && pi != i {
			children[pi] = append(children[pi], i)
			continue
		}
		roots = append(roots, i)
	}
	if len(children) == 0 {
		return groups
	}
	var build func(i, depth int) webLayerGroup
	build = func(i, depth int) webLayerGroup {
		g := groups[i]
		if depth <= webMaxLayerDepth {
			for _, c := range children[i] {
				g.nested = append(g.nested, build(c, depth+1))
			}
		}
		return g
	}
	out := make([]webLayerGroup, 0, len(roots))
	for _, i := range roots {
		out = append(out, build(i, 0))
	}
	return out
}

// layoutLayer measures one positioned subtree (or one lone positioned block),
// places it against its containing block and adds it to the layer list. It
// returns the page y of the layer's bottom edge, or 0 when nothing was placed.
// parked is where the next box placed against the page's bottom goes: after
// the last line, one below the other, so a cookie bar covers no text.
func (l *webLayouter) layoutLayer(g webLayerGroup, padX, contentW, pageH int32, parked *int32) int32 {
	if len(g.blocks) == 0 {
		return 0
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	first := g.blocks[0]
	var info webLayerInfo
	layered := g.layer > 0 && l.doc != nil && g.layer <= len(l.doc.Layers)
	if layered {
		info = l.doc.Layers[g.layer-1]
	} else {
		info = webLayerInfo{style: first.Style, outerBoxes: len(first.Boxes), key: webFreeStackKey(first.ZIndex, first.TreeSeq)}
		if first.OwnsBox && info.outerBoxes > 0 {
			info.outerBoxes--
		}
		if first.Image {
			// An image block's style is the picture's; its own fields hold
			// the positioned element's insets and z-index.
			s := &info.style
			s.Top, s.Right, s.Bottom, s.Left = first.PosTop, first.PosRight, first.PosBottom, first.PosLeft
			s.HasTop, s.HasRight, s.HasBottom, s.HasLeft = first.HasTop, first.HasRight, first.HasBottom, first.HasLeft
			s.ZIndex = first.ZIndex
		}
	}
	st := info.style
	pageW := contentW + 2*padX
	// The containing block is found from a block that carries the root's
	// whole box chain (a table's row marker or a rule carries none).
	for _, b := range g.blocks {
		if len(b.Boxes) >= info.outerBoxes {
			first = b
			break
		}
	}
	anchor, onPage, clip, hasClip := l.layerAnchor(first, info, padX, contentW, pageH)
	if layered && info.parent != 0 {
		// Inside another layer the containing block is that layer's box, never
		// the page: a caption at `bottom: 0` sits at its card's foot, not after
		// it.
		onPage = false
	}

	// The layer's width: authored, stretched between two insets, or shrunk
	// to its widest line within the room its inset leaves.
	insetL := webInsetPx(st.Left, st.InsetPct[3], anchor.W, scale)
	insetR := webInsetPx(st.Right, st.InsetPct[1], anchor.W, scale)
	stretch := st.Width <= 0 && st.WidthPct <= 0 && st.HasLeft && st.HasRight
	var w int32
	switch {
	case st.WidthPct > 0:
		w = int32(math.Round(float64(anchor.W) * st.WidthPct / 100))
	case st.Width > 0:
		w = int32(math.Round(st.Width * scale))
	case stretch:
		w = anchor.W - insetL - insetR
	default:
		room := anchor.W
		switch {
		case st.HasLeft:
			room -= insetL
		case st.HasRight:
			room -= insetR
		}
		if room < 60 {
			room = min32(60, pageW)
		}
		w = l.intrinsicContainerWidth(g.blocks)
		if w > 0 && !(first.OwnsBox && info.rootBox != 0 && first.BoxOwner == info.rootBox) {
			// The text sits in the root's children, so the root's own
			// padding is not in their measure yet.
			w += int32(math.Round((st.Indent + st.IndentRight) * scale))
		}
		if w <= 0 || w > room {
			w = room
		}
	}
	if maxW := webConstraintLength(st.MaxWidth, st.MaxWidthPct, anchor.W, scale); maxW > 0 && float64(w)/scale > maxW {
		w = int32(math.Round(maxW * scale))
	}
	if minW := webConstraintLength(st.MinWidth, st.MinWidthPct, anchor.W, scale); minW > 0 && float64(w)/scale < minW {
		w = int32(math.Round(minW * scale))
	}
	if !stretch {
		// A box narrower than its longest word would set a letter a line.
		if floor := min32(60, l.intrinsicMinContentWidth(g.blocks)); w < floor {
			w = floor
		}
	}
	w = min32(max32(w, 1), pageW)

	// The subtree is laid out at that width, as ordinary flow inside the
	// layer. The root's width was spent above, so its own box keeps only its
	// padding; the flex containers outside the layer are not its parents.
	blocks := make([]webBlock, len(g.blocks))
	copy(blocks, g.blocks)
	for i := range blocks {
		b := &blocks[i]
		// An image block's style is the picture's own, never the root's.
		if !b.Image && (!layered || (b.OwnsBox && b.BoxOwner == info.rootBox)) {
			b.Width, b.WidthPct = 0, 0
			b.Style.Width, b.Style.WidthPct = 0, 0
			b.Style.MinWidth, b.Style.MaxWidth = 0, 0
			b.Style.MinWidthPct, b.Style.MaxWidthPct = 0, 0
			b.Style.MarginLeftAuto, b.Style.MarginRightAuto = false, false
		}
		if !layered {
			continue
		}
		b.Float = false
		for k := 0; k < info.outerFlex; k++ {
			if len(b.FlexTrail) == 0 {
				b.FlexGroup, b.FlexItem = 0, 0
				break
			}
			b.setFlexMembership(b.FlexTrail[0])
			b.FlexTrail = b.FlexTrail[1:]
		}
	}
	// An absolute box held by both its top and its bottom inset, with no
	// height of its own, is as tall as the room between them (inset: 0): an
	// overlay over its photo card, a scrim, a side rail. Its own flex or grid
	// container is given that height, so its content sits where the box puts
	// it — the caption at the foot of the photo, the play button in the middle
	// (it used to hang at the top) — and its surface fills the room below.
	roomH := int32(0)
	if layered && st.Position == webPositionAbsolute && st.HasTop && st.HasBottom && st.Height <= 0 &&
		!(st.MarginTopAuto && st.MarginBotAuto) {
		roomH = anchor.H - webInsetPx(st.Top, st.InsetPct[0], anchor.H, scale) - webInsetPx(st.Bottom, st.InsetPct[2], anchor.H, scale)
		if css := float64(roomH)/scale - st.PadTop - st.PadBot; css > 0 && info.flexGroup != 0 {
			for i := range blocks {
				if blocks[i].FlexGroup == info.flexGroup && css > blocks[i].FlexHeight {
					blocks[i].FlexHeight = css
				}
			}
		}
		// A box that is only a picture (`absolute inset-0` around an `h-full
		// w-full object-cover` image: a hero's or a card's backdrop) fills its
		// room with it, cropped as its object-fit says. The picture's own
		// height: 100% was lost on the way (the box had no definite height
		// while the page was read), so it used to keep its natural height and
		// end above the hero's foot or hang past it. A room of a line or two
		// (a box only a spacer measures) keeps the natural size.
		if css := float64(roomH)/scale - st.PadTop - st.PadBot; css > 0 && roomH >= 3*l.rowH {
			picture := -1
			for i := range blocks {
				b := &blocks[i]
				switch {
				case b.Image && picture < 0 && b.Style.Height <= 0:
					picture = i
				case b.Image || webBlockHasText(b):
					picture = -2
				}
				if picture == -2 {
					break
				}
			}
			if picture >= 0 {
				im := &blocks[picture].Style
				im.Height = css
				if im.Width <= 0 && im.WidthPct <= 0 {
					im.WidthPct = 100
				}
			}
		}
	}
	sub := &webLayout{RowH: l.rowH}
	sl := &webLayouter{
		doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
		spaceWidths: l.spaceWidths, sanitized: l.sanitized,
		containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan),
	}
	if layered {
		sl.constraintSkip = info.outerBoxes
		sl.boxFloor = info.rootBox
		if root, ok := l.doc.Boxes[info.rootBox]; ok && info.rootBox != 0 {
			root.style.Width, root.style.WidthPct = 0, 0
			root.style.MinWidth, root.style.MaxWidth = 0, 0
			root.style.MinWidthPct, root.style.MaxWidthPct = 0, 0
			root.style.MarginLeftAuto, root.style.MarginRightAuto = false, false
			sl.boxOverride = map[int]webBoxInfo{info.rootBox: root}
		}
		sl.layoutBlocks(blocks, 0, w)
		if info.rootBox != 0 {
			// The layer's own cards and its root's surface, painted inside
			// the layer; the page's container pass leaves layers alone.
			sl.paintContainerBoxes()
		}
	} else {
		sl.constraintSkip = l.constraintSkip
		sl.layoutBlocksFlat(blocks, 0, w)
	}
	l.absorbContainerRows(sl.containerRows)
	if len(sub.Rows) == 0 && len(sub.Rects) == 0 && len(sub.Images) == 0 && len(sub.Backgrounds) == 0 && len(sub.Videos) == 0 &&
		len(g.nested) == 0 {
		return 0
	}
	webSortPaintOrder(sub)
	h := webLayerExtent(sub)
	if h <= 0 {
		h = l.rowH
	}
	if roomH > h {
		webStretchLayerSurface(sub, info.rootBox, h, roomH)
		h = roomH
	}
	// The layers inside this one: placed against the boxes this layer just
	// measured (its root, a positioned card in it), they become floats of its
	// sub-layout, which follow it below — moved with it, painted over it.
	if len(g.nested) > 0 {
		parkedIn := h
		for _, child := range g.nested {
			sl.layoutLayer(child, 0, w, h, &parkedIn)
		}
	}

	// Percentage insets resolve against the containing block (a fixed box's
	// vertical ones against the screen), and translate() moves the box by
	// lengths and by percentages of its own size: the `left: 50%; transform:
	// translate(-50%, -50%)` centring idiom.
	// A fixed box inside another layer stays with that layer.
	fixed := st.Position == webPositionFixed && info.parent == 0
	vertical := anchor.H
	if fixed {
		vertical = webViewHeightPx(scale, pageH)
	}
	insetT := webInsetPx(st.Top, st.InsetPct[0], vertical, scale)
	insetB := webInsetPx(st.Bottom, st.InsetPct[2], vertical, scale)
	if fixed && st.HasBottom && !st.HasTop && insetB < 0 && -insetB >= h {
		// Parked wholly below the screen's bottom edge until a script lifts it
		// (Wikipedia's donation bar is `position: fixed; bottom: -500px` until
		// it opens): a browser shows nothing of it. Too tall to pin, it used to
		// be laid in after the page's text instead.
		return 0
	}
	static, hasStatic := l.layout.statics[g.layer]
	if g.layer == 0 {
		hasStatic = false
	}

	// A fixed layer holds to the screen: at its top or bottom inset, centred
	// by auto margins, or (without insets) at its static place at scroll 0.
	// One too big to pin — taller than the view, or most of it across most
	// of the width (a drawer, a consent panel) — would hide the page for good
	// and scrolls with it instead.
	pin := webPinNone
	if fixed && layered {
		switch {
		case st.HasTop && st.HasBottom && st.MarginTopAuto && st.MarginBotAuto:
			pin = webPinCenter
		case st.HasTop || !st.HasBottom:
			pin = webPinTop
		default:
			pin = webPinBottom
		}
		if h > vertical || (h*2 > vertical && w*5 > pageW*4) {
			pin = webPinNone
		}
	}

	x := anchor.X
	switch {
	case st.HasLeft && st.HasRight && !stretch && st.MarginLeftAuto && st.MarginRightAuto:
		// Both insets, a width and auto margins: the centred dialog idiom.
		x = anchor.X + insetL + (anchor.W-insetL-insetR-w)/2
	case st.HasLeft:
		x = anchor.X + insetL
	case st.HasRight:
		x = anchor.X + anchor.W - w - insetR
	case hasStatic:
		// No horizontal inset: the box stays where its markup put it.
		x = static.X
	}
	y, bottom := anchor.Y, int32(0)
	switch {
	case pin == webPinBottom:
		bottom = insetB
	case pin == webPinCenter:
		y = (insetT - insetB) / 2
	case st.HasTop && st.HasBottom && st.MarginTopAuto && st.MarginBotAuto:
		y = anchor.Y + insetT + (vertical-insetT-insetB-h)/2
	case st.HasTop:
		y = anchor.Y + insetT
	case st.HasBottom && onPage:
		// Against the bottom of a screen the reader is not pinning it to:
		// after the page's last line instead of over it.
		y = *parked
	case st.HasBottom:
		y = anchor.Y + anchor.H - h - insetB
	case hasStatic:
		// No vertical inset: its static position (CSS keeps it there).
		y = static.Y
	}
	if st.HasTranslate {
		dx := int32(math.Round(st.TranslateX*scale + st.TranslatePctX*float64(w)/100))
		dy := int32(math.Round(st.TranslateY*scale + st.TranslatePctY*float64(h)/100))
		if st.HasBottom && !st.HasTop && (onPage || pin == webPinBottom) && dy >= h {
			// Slid below the screen's bottom edge until a script brings it
			// up: a cookie bar or a toast waiting to slide in.
			return 0
		}
		x += dx
		if pin == webPinBottom {
			bottom -= dy
		} else {
			y += dy
		}
	}
	if x+w <= 0 || x >= pageW || (pin != webPinBottom && y+h <= 0) {
		// Wholly off the page or the screen: a skip link parked above it, an
		// off-canvas drawer slid out of view. A browser shows nothing of it.
		return 0
	}
	if y < 0 && pin == webPinNone {
		y = 0
	}

	f := webFloat{
		Z: st.ZIndex, X: x, Y: y, H: h, W: w, Pin: pin, Bottom: bottom,
		Phase: webLayerPhaseFor(info.key, st.ZIndex), Key: info.key, Root: info.rootBox,
		Rows: sub.Rows, Rects: sub.Rects, Backgrounds: sub.Backgrounds,
		Images: sub.Images, Videos: sub.Videos,
	}
	if hasClip && pin == webPinNone {
		// An overflow: hidden containing block, or a box around it, cuts its
		// layers to its box (layerClip); a layer wholly outside it (a
		// carousel's next slide) is dropped.
		if !webRectsOverlap(sdl.Rect{X: x, Y: y, W: w, H: h}, clip) {
			return 0
		}
		f.Clip, f.HasClip = clip, true
	}
	l.layout.Floats = append(l.layout.Floats, f)
	if st.HasBottom && !st.HasTop && onPage && pin == webPinNone && y == *parked {
		*parked = y + h
	}
	// A float inside the layer paints with the layer, at its place in the
	// stack (and on the screen when the layer is pinned), not with the page's
	// floats under the text.
	for _, sf := range sub.Floats {
		local := sf.Y
		sfH := sf.H
		if sfH <= 0 {
			sfH = int32(len(sf.Rows)) * l.rowH
		}
		sf.X += x
		sf.Y = local + y
		sf.Phase, sf.Key, sf.Pin, sf.Root = f.Phase, f.Key, f.Pin, f.Root
		switch f.Pin {
		case webPinBottom:
			sf.Bottom = bottom + h - (local + sfH)
		case webPinCenter:
			sf.Y = y + local + (sfH-h)/2
		}
		if f.HasClip {
			sf.Clip, sf.HasClip = f.Clip, true
		}
		l.layout.Floats = append(l.layout.Floats, sf)
	}
	if pin != webPinNone {
		// On the screen, not on the page: it does not lengthen the page.
		return 0
	}
	return y + h
}

// webViewHeightPx is the height of the screen the page is read on, in layout
// pixels: what a fixed box's percentages and vh units resolve against.
func webViewHeightPx(scale float64, fallback int32) int32 {
	if _, vh := webMediaViewport(); vh > 0 {
		return int32(vh * scale)
	}
	return fallback
}

// layerAnchor finds a layer's containing block: the laid-out rectangle of
// the nearest positioned ancestor that has one, the page column when there is
// none, and the whole page width for a fixed box (its containing block is the
// viewport). onPage reports the page; clip is the overflow clip the layer is
// cut to, when hasClip (layerClip).
func (l *webLayouter) layerAnchor(first webBlock, info webLayerInfo, padX, contentW, pageH int32) (anchor sdl.Rect, onPage bool, clip sdl.Rect, hasClip bool) {
	if info.style.Position == webPositionFixed {
		return sdl.Rect{X: 0, Y: 0, W: contentW + 2*padX, H: pageH}, true, sdl.Rect{}, false
	}
	page := sdl.Rect{X: padX, Y: 0, W: contentW, H: pageH}
	if l.doc == nil {
		return page, true, sdl.Rect{}, false
	}
	for i := min(info.outerBoxes, len(first.Boxes)) - 1; i >= 0; i-- {
		id := first.Boxes[i]
		box, ok := l.doc.Boxes[id]
		if !ok || !webStyleIsAnchor(&box.style) {
			continue
		}
		// An ancestor with nothing laid out to measure hands over to the
		// next one out, which contains it.
		if rect, found := l.boxFlowRect(id); found {
			// A wrapper that spans the whole page (a positioned <body>, a
			// theme's #page) is the page itself.
			onPage := rect.Y <= l.rowH && rect.Y+rect.H >= pageH-2*l.rowH
			clip, hasClip := l.layerClip(first.Boxes[:i+1], id, rect)
			return rect, onPage, clip, hasClip
		}
	}
	return page, true, sdl.Rect{}, false
}

// layerClip is the overflow clip of a layer whose containing block is the
// last of boxes (anchor, laid out at anchorRect): the containing block itself
// and every box around it with overflow hidden or clip cut the layer on that
// axis. A carousel's frame clips the captions of the slides inside its moving
// track, which is their containing block; a box between the containing block
// and the layer does not clip it, as in CSS.
func (l *webLayouter) layerClip(boxes []int, anchor int, anchorRect sdl.Rect) (sdl.Rect, bool) {
	const far = 1 << 20
	left, top, right, bottom := int32(-far), int32(-far), int32(far), int32(far)
	has := false
	for _, id := range boxes {
		box, ok := l.doc.Boxes[id]
		if !ok || (box.style.OverflowX == 0 && box.style.OverflowY == 0) {
			continue
		}
		rect, found := anchorRect, true
		if id != anchor {
			rect, found = l.boxFlowRect(id)
		}
		if !found {
			continue
		}
		if box.style.OverflowX != 0 {
			left, right = max(left, rect.X), min(right, rect.X+rect.W)
		}
		if box.style.OverflowY != 0 {
			top, bottom = max(top, rect.Y), min(bottom, rect.Y+rect.H)
		}
		has = true
	}
	if !has {
		return sdl.Rect{}, false
	}
	return sdl.Rect{X: left, Y: top, W: max(right-left, 0), H: max(bottom-top, 0)}, true
}

// webLayerExtent is how far a measured layer reaches below its top: its rows,
// and any surface, picture or play card past them.
func webLayerExtent(sub *webLayout) int32 {
	h := int32(len(sub.Rows)) * sub.RowH
	for _, bg := range sub.Backgrounds {
		if !bg.Shadow {
			h = max(h, bg.Rect.Y+bg.Rect.H)
		}
	}
	for _, r := range sub.Rects {
		if !r.Shadow {
			h = max(h, r.Rect.Y+r.Rect.H)
		}
	}
	for _, im := range sub.Images {
		h = max(h, im.Rect.Y+im.Rect.H)
	}
	for _, v := range sub.Videos {
		h = max(h, v.Rect.Y+v.Rect.H)
	}
	return h
}

// webStretchLayerSurface grows a layer measured `from` pixels tall to `to`:
// its root's own surface — the fill, the background layers, the border box,
// the side bars, the shadow — reaches the new bottom, and a bottom border bar
// moves down to it. The content stays where the layout put it.
func webStretchLayerSurface(sub *webLayout, rootBox int, from, to int32) {
	if sub == nil || rootBox == 0 || to <= from {
		return
	}
	extra := to - from
	grow := func(r *sdl.Rect) {
		if r.Y+r.H < from-2 {
			// It ends above the old bottom: not a surface of the whole box.
			return
		}
		if r.Y <= sub.RowH {
			r.H += extra
		} else {
			r.Y += extra
		}
	}
	for i := range sub.Rects {
		if r := &sub.Rects[i]; r.BoxID == rootBox && !r.Inner {
			grow(&r.Rect)
		}
	}
	for i := range sub.Backgrounds {
		if bg := &sub.Backgrounds[i]; bg.BoxID == rootBox && !bg.Inner {
			grow(&bg.Rect)
		}
	}
}

// webRectsOverlap reports whether two rectangles share any area.
func webRectsOverlap(a, b sdl.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// webBoxesHave reports whether a chain of box IDs contains id.
func webBoxesHave(boxes []int, id int) bool {
	for _, box := range boxes {
		if box == id {
			return true
		}
	}
	return false
}

// webSortPaintOrder puts a layout's surfaces in CSS paint order: by z-index,
// then in tree order — a box's own surface first, then what the content
// inside it paints (an image's frame), then the boxes nested in it. Box IDs
// are handed out in document order, which is tree order, so an ancestor's
// background no longer covers a descendant's.
func webSortPaintOrder(layout *webLayout) {
	if layout == nil {
		return
	}
	sort.SliceStable(layout.Rects, func(i, j int) bool {
		a, b := &layout.Rects[i], &layout.Rects[j]
		if a.Z != b.Z {
			return a.Z < b.Z
		}
		return webPaintTree(a.BoxID, a.Inner) < webPaintTree(b.BoxID, b.Inner)
	})
	sort.SliceStable(layout.Backgrounds, func(i, j int) bool {
		a, b := &layout.Backgrounds[i], &layout.Backgrounds[j]
		if a.Z != b.Z {
			return a.Z < b.Z
		}
		return webPaintTree(a.BoxID, a.Inner) < webPaintTree(b.BoxID, b.Inner)
	})
}

// webPaintTree is a paint's place in tree order: its box's own surface, or
// the content painted inside that box.
func webPaintTree(box int, inner bool) int {
	if inner {
		return box*2 + 1
	}
	return box * 2
}

// webCollectStickies builds the pinned copy of every sticky element once the
// page is laid out and its boxes are painted: the runs, surfaces and pictures
// of the element's whole subtree, found by box membership (the element's box
// and every box opened inside it), in the copy's own coordinates with Y its
// place in the flow. A header made of a logo, a menu and buttons is pinned as
// the bar it is — the reader used to pin only an element whose own text was
// the bar — and so is a table's header cell. Each bar stays inside its limit
// box (its parent, or its table), and the bars are put in stacking order.
func webCollectStickies(d *webDocument, layout *webLayout, l *webLayouter) {
	if layout == nil {
		return
	}
	layout.Stickies = nil
	if d == nil || len(d.Stickies) == 0 || layout.RowH <= 0 {
		return
	}
	n := len(d.Stickies)
	roots := make(map[int]int, n)
	for i := range d.Stickies {
		roots[d.Stickies[i].rootBox] = i
	}
	// The outermost sticky a chain of boxes runs through (a root's box is in
	// every chain under it), or -1.
	ownerOfChain := func(boxes []int) int {
		for _, id := range boxes {
			if i, ok := roots[id]; ok {
				return i
			}
		}
		return -1
	}
	// The outermost sticky whose subtree holds box id, or -1. Stickies are in
	// document order, which is the order of their root boxes.
	ownerOfBox := func(id int) int {
		if id == 0 {
			return -1
		}
		for i := range d.Stickies {
			s := &d.Stickies[i]
			if s.rootBox > id {
				break
			}
			if id <= s.lastBox {
				return i
			}
		}
		return -1
	}
	type stickyAcc struct {
		rows       []webRow
		rects      []webRect
		bgs        []webBackgroundPaint
		images     []webImagePos
		minX, minY int32
		maxX, maxY int32
		seen       bool
	}
	accs := make([]stickyAcc, n)
	extend := func(a *stickyAcc, r sdl.Rect) {
		if r.W <= 0 && r.H <= 0 {
			return
		}
		if !a.seen {
			a.minX, a.minY, a.maxX, a.maxY, a.seen = r.X, r.Y, r.X+r.W, r.Y+r.H, true
			return
		}
		a.minX, a.minY = min(a.minX, r.X), min(a.minY, r.Y)
		a.maxX, a.maxY = max(a.maxX, r.X+r.W), max(a.maxY, r.Y+r.H)
	}
	var touched []int
	for ri := range layout.Rows {
		row := &layout.Rows[ri]
		touched = touched[:0]
		for _, run := range row.Runs {
			owner := ownerOfChain(run.Boxes)
			if owner < 0 {
				continue
			}
			a := &accs[owner]
			if k := len(a.rows); k == 0 || a.rows[k-1].Y != row.Y || !webIntsHave(touched, owner) {
				a.rows = append(a.rows, webRow{Y: row.Y})
				touched = append(touched, owner)
			}
			last := &a.rows[len(a.rows)-1]
			last.Runs = append(last.Runs, run)
			w, h := run.ImageW, run.ImageH
			if run.Image == nil && run.Font != nil {
				if tw, th, err := run.Font.SizeUTF8(run.Text); err == nil {
					w, h = int32(tw), int32(th)
				}
			}
			extend(a, sdl.Rect{X: run.X, Y: row.Y + run.VerticalOffset, W: max(w, 1), H: max(h, 1)})
		}
	}
	for _, r := range layout.Rects {
		if owner := ownerOfBox(r.BoxID); owner >= 0 {
			accs[owner].rects = append(accs[owner].rects, r)
			if !r.Shadow {
				extend(&accs[owner], r.Rect)
			}
		}
	}
	for _, bg := range layout.Backgrounds {
		if owner := ownerOfBox(bg.BoxID); owner >= 0 {
			accs[owner].bgs = append(accs[owner].bgs, bg)
			if !bg.Shadow {
				extend(&accs[owner], bg.Rect)
			}
		}
	}
	for _, im := range layout.Images {
		if owner := ownerOfChain(im.Boxes); owner >= 0 {
			accs[owner].images = append(accs[owner].images, im)
			extend(&accs[owner], im.Rect)
		}
	}
	// The layers and floats inside a sticky element are part of its bar: a
	// header's absolutely positioned logo, cart badge or menu button, a
	// floated search box. They are copied in at their place on the page (the
	// bar used to pin without them), and a layer is not painted a second time
	// at its page place while its bar is pinned (webFloat.StickyIndex).
	type stickyLayer struct{ float, owner int }
	var attached []stickyLayer
	ownSeen := make([]bool, n)
	for i := range accs {
		ownSeen[i] = accs[i].seen
	}
	for fi := range layout.Floats {
		lf := &layout.Floats[fi]
		if lf.Root == 0 || lf.Pin != webPinNone || lf.Lift {
			continue
		}
		owner := ownerOfBox(lf.Root)
		if owner < 0 {
			continue
		}
		a := &accs[owner]
		for _, row := range lf.Rows {
			copied := webRow{Y: lf.Y + row.Y, Runs: make([]webRunPos, len(row.Runs))}
			for k, run := range row.Runs {
				run.X += lf.X
				copied.Runs[k] = run
			}
			a.rows = append(a.rows, copied)
		}
		for _, r := range lf.Rects {
			r.Rect.X, r.Rect.Y = r.Rect.X+lf.X, r.Rect.Y+lf.Y
			a.rects = append(a.rects, r)
		}
		for _, bg := range lf.Backgrounds {
			bg.Rect.X, bg.Rect.Y = bg.Rect.X+lf.X, bg.Rect.Y+lf.Y
			a.bgs = append(a.bgs, bg)
		}
		for _, im := range lf.Images {
			im.Rect.X, im.Rect.Y = im.Rect.X+lf.X, im.Rect.Y+lf.Y
			im.HasClip = false
			a.images = append(a.images, im)
		}
		if !ownSeen[owner] {
			// A bar made only of layers is as big as they are. Otherwise the
			// bar keeps its own size, so a dropdown hanging below a header
			// does not stop it pinning; it hangs below the pinned bar too.
			h := lf.H
			if h <= 0 {
				h = int32(len(lf.Rows)) * layout.RowH
			}
			extend(a, sdl.Rect{X: lf.X, Y: lf.Y, W: max(lf.W, 1), H: max(h, 1)})
		}
		attached = append(attached, stickyLayer{float: fi, owner: owner})
	}
	scale := 1.0
	if l != nil && l.baseSize > 0 {
		scale = l.baseSize / 16
	}
	limits := make(map[int]sdl.Rect, 4)
	for i := range accs {
		a := &accs[i]
		if !a.seen || len(a.rows) == 0 && len(a.bgs) == 0 {
			continue
		}
		s := &d.Stickies[i]
		// A bar at the very top of the page can start above it (its border
		// or padding over the first line); it pins only once scrolled.
		top := max(a.minY, 0)
		f := webFloat{
			Z: s.style.ZIndex, Y: top, H: a.maxY - top, W: a.maxX - a.minX,
			Key: webFreeStackKey(s.style.ZIndex, s.seq), LimitBox: s.limitBox, Root: s.rootBox,
		}
		// Rows from the bar's layers came after its own: the painter walks
		// them top to bottom.
		sort.SliceStable(a.rows, func(i, j int) bool { return a.rows[i].Y < a.rows[j].Y })
		if s.style.HasTop {
			f.Top = max(0, int32(math.Round(s.style.Top*scale)))
		}
		if s.style.HasBottom && !s.style.HasTop {
			// bottom alone sticks the bar to the bottom edge instead.
			f.HasBottom = true
			f.Bottom = max(0, int32(math.Round(s.style.Bottom*scale)))
		}
		for _, row := range a.rows {
			row.Y -= f.Y
			f.Rows = append(f.Rows, row)
		}
		for _, r := range a.rects {
			r.Rect.Y -= f.Y
			f.Rects = append(f.Rects, r)
		}
		for _, bg := range a.bgs {
			bg.Rect.Y -= f.Y
			f.Backgrounds = append(f.Backgrounds, bg)
		}
		for _, im := range a.images {
			im.Rect.Y -= f.Y
			f.Images = append(f.Images, im)
		}
		if f.LimitBox != 0 && l != nil {
			rect, ok := limits[f.LimitBox]
			if !ok {
				if found, measured := l.boxFlowRect(f.LimitBox); measured {
					rect = found
				}
				limits[f.LimitBox] = rect
			}
			// A limit box measured shorter than the bar would pin it nowhere;
			// such a bar keeps the page as its limit.
			if rect.H > 0 && rect.Y+rect.H >= f.Y+f.H {
				f.LimitTop, f.Limit = rect.Y, rect.Y+rect.H
			}
		}
		layout.Stickies = append(layout.Stickies, f)
	}
	sort.SliceStable(layout.Stickies, func(i, j int) bool {
		return layout.Stickies[i].Key.less(layout.Stickies[j].Key)
	})
	for _, at := range attached {
		root := d.Stickies[at.owner].rootBox
		for si := range layout.Stickies {
			if layout.Stickies[si].Root == root {
				layout.Floats[at.float].StickyIndex = si + 1
				break
			}
		}
	}
}

// webLiftPositioned paints the relatively positioned elements of the flow
// where CSS stacks them. A browser paints a positioned element with
// everything in it in the positioned pass, in stacking order with the
// absolutely positioned layers; the reader painted the whole flow first and
// every layer over it. So the hero idiom — a picture or a scrim in an
// `absolute inset-0` box, then the heading and buttons in a `relative z-10`
// box — showed the picture over the heading. An element that comes after a
// layer in stacking order (a higher z-index, or the same and later in the
// page) and overlaps it is lifted: its words, surfaces and pictures are
// painted as a layer of their own at their place in that order (webFloat.
// Lift), and the flow's paint skips them (Lifted). The page's rows keep its
// words, so links, find and the cursor work as before. An element inside a
// lifted one goes with it; one that overlaps no such layer is left alone.
func webLiftPositioned(d *webDocument, layout *webLayout) {
	if d == nil || layout == nil || layout.RowH <= 0 || len(d.Positioned) == 0 || len(layout.Floats) == 0 {
		return
	}
	type over struct {
		rect sdl.Rect
		key  webStackKey
	}
	var layers []over
	for i := range layout.Floats {
		f := &layout.Floats[i]
		if f.Phase != webLayerAbove || f.Pin != webPinNone || f.Lift {
			continue
		}
		h := f.H
		if h <= 0 {
			h = int32(len(f.Rows)) * layout.RowH
		}
		r := sdl.Rect{X: f.X, Y: f.Y, W: max(f.W, 1), H: max(h, 1)}
		if f.HasClip {
			left, top := max(r.X, f.Clip.X), max(r.Y, f.Clip.Y)
			right, bottom := min(r.X+r.W, f.Clip.X+f.Clip.W), min(r.Y+r.H, f.Clip.Y+f.Clip.H)
			if right <= left || bottom <= top {
				continue
			}
			r = sdl.Rect{X: left, Y: top, W: right - left, H: bottom - top}
		}
		layers = append(layers, over{rect: r, key: f.Key})
	}
	if len(layers) == 0 {
		return
	}
	n := len(d.Positioned)
	roots := make(map[int]int, n)
	parent := make([]int, n)
	var open []int
	for i := range d.Positioned {
		e := &d.Positioned[i]
		roots[e.rootBox] = i
		for len(open) > 0 && d.Positioned[open[len(open)-1]].lastBox < e.rootBox {
			open = open[:len(open)-1]
		}
		parent[i] = -1
		if len(open) > 0 {
			parent[i] = open[len(open)-1]
		}
		open = append(open, i)
	}
	// The innermost positioned element a chain of boxes runs through, or
	// one whose subtree holds box id; -1 when there is none.
	ownerOfChain := func(boxes []int) int {
		for k := len(boxes) - 1; k >= 0; k-- {
			if i, ok := roots[boxes[k]]; ok {
				return i
			}
		}
		return -1
	}
	ownerOfBox := func(id int) int {
		if id == 0 {
			return -1
		}
		i := sort.Search(n, func(k int) bool { return d.Positioned[k].rootBox > id }) - 1
		for i >= 0 && d.Positioned[i].lastBox < id {
			i = parent[i]
		}
		return i
	}
	// Each element's extent on the page, its nested elements' included.
	type extent struct {
		minX, minY, maxX, maxY int32
		seen                   bool
	}
	ext := make([]extent, n)
	grow := func(i int, r sdl.Rect) {
		for ; i >= 0; i = parent[i] {
			e := &ext[i]
			if !e.seen {
				*e = extent{minX: r.X, minY: r.Y, maxX: r.X + r.W, maxY: r.Y + r.H, seen: true}
				continue
			}
			e.minX, e.minY = min(e.minX, r.X), min(e.minY, r.Y)
			e.maxX, e.maxY = max(e.maxX, r.X+r.W), max(e.maxY, r.Y+r.H)
		}
	}
	for ri := range layout.Rows {
		row := &layout.Rows[ri]
		for k := 0; k < len(row.Runs); {
			// A stretch of runs with one owner is measured once, at its
			// rightmost run.
			o := ownerOfChain(row.Runs[k].Boxes)
			j := k + 1
			for j < len(row.Runs) && ownerOfChain(row.Runs[j].Boxes) == o {
				j++
			}
			if o >= 0 {
				last := &row.Runs[j-1]
				w, h := last.ImageW, last.ImageH
				if last.Image == nil && last.Font != nil {
					if tw, th, err := last.Font.SizeUTF8(last.Text); err == nil {
						w, h = int32(tw), int32(th)
					}
				}
				h = max(h+last.VerticalOffset, layout.RowH)
				grow(o, sdl.Rect{X: row.Runs[k].X, Y: row.Y, W: max(last.X+w-row.Runs[k].X, 1), H: h})
			}
			k = j
		}
	}
	for _, r := range layout.Rects {
		if o := ownerOfBox(r.BoxID); o >= 0 && !r.Shadow {
			grow(o, r.Rect)
		}
	}
	for _, bg := range layout.Backgrounds {
		if o := ownerOfBox(bg.BoxID); o >= 0 && !bg.Shadow {
			grow(o, bg.Rect)
		}
	}
	for _, im := range layout.Images {
		if o := ownerOfChain(im.Boxes); o >= 0 {
			grow(o, im.Rect)
		}
	}
	// Which elements are lifted: the outermost ones that come after a layer
	// they overlap. liftRoot maps every element to the lifted one it is
	// painted with, or -1.
	liftRoot := make([]int, n)
	lifted := 0
	for i := 0; i < n; i++ {
		liftRoot[i] = -1
		if parent[i] >= 0 {
			liftRoot[i] = liftRoot[parent[i]]
		}
		e := &ext[i]
		if liftRoot[i] >= 0 || !e.seen {
			continue
		}
		box := sdl.Rect{X: e.minX, Y: e.minY, W: max(e.maxX-e.minX, 1), H: max(e.maxY-e.minY, 1)}
		for _, layer := range layers {
			if layer.key.less(d.Positioned[i].key) && webRectsOverlap(layer.rect, box) {
				liftRoot[i] = i
				lifted++
				break
			}
		}
	}
	if lifted == 0 {
		return
	}
	floats := make(map[int]*webFloat, lifted)
	floatOf := func(o int) *webFloat {
		if o < 0 || liftRoot[o] < 0 {
			return nil
		}
		root := liftRoot[o]
		if f, ok := floats[root]; ok {
			return f
		}
		e := &ext[root]
		key := d.Positioned[root].key
		f := &webFloat{
			Y: e.minY, H: max(e.maxY-e.minY, 1), W: max(e.maxX, 1),
			Phase: webLayerAbove, Key: key, Z: key.levels[key.n-1].z, Lift: true,
			Root: d.Positioned[root].rootBox,
		}
		floats[root] = f
		return f
	}
	for ri := range layout.Rows {
		row := &layout.Rows[ri]
		for k := range row.Runs {
			run := &row.Runs[k]
			f := floatOf(ownerOfChain(run.Boxes))
			if f == nil {
				continue
			}
			if len(f.Rows) == 0 || f.Rows[len(f.Rows)-1].Y != row.Y-f.Y {
				f.Rows = append(f.Rows, webRow{Y: row.Y - f.Y})
			}
			cp := *run
			if cp.HasClip {
				cp.Clip.Y -= f.Y
			}
			last := &f.Rows[len(f.Rows)-1]
			last.Runs = append(last.Runs, cp)
			run.Lifted = true
		}
	}
	for i := range layout.Rects {
		r := &layout.Rects[i]
		if f := floatOf(ownerOfBox(r.BoxID)); f != nil {
			cp := *r
			cp.Rect.Y -= f.Y
			if cp.HasClip {
				cp.Clip.Y -= f.Y
			}
			f.Rects = append(f.Rects, cp)
			r.Lifted = true
		}
	}
	for i := range layout.Backgrounds {
		bg := &layout.Backgrounds[i]
		if f := floatOf(ownerOfBox(bg.BoxID)); f != nil {
			cp := *bg
			cp.Rect.Y -= f.Y
			f.Backgrounds = append(f.Backgrounds, cp)
			bg.Lifted = true
		}
	}
	for i := range layout.Images {
		im := &layout.Images[i]
		if f := floatOf(ownerOfChain(im.Boxes)); f != nil {
			cp := *im
			cp.Rect.Y -= f.Y
			if cp.HasClip {
				cp.Clip.Y -= f.Y
			}
			f.Images = append(f.Images, cp)
			im.Lifted = true
		}
	}
	for i := 0; i < n; i++ {
		if f, ok := floats[i]; ok {
			layout.Floats = append(layout.Floats, *f)
		}
	}
	sort.SliceStable(layout.Floats, func(i, j int) bool {
		a, b := &layout.Floats[i], &layout.Floats[j]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		return a.Key.less(b.Key)
	})
}

// webIntsHave reports whether a short list of ints contains v.
func webIntsHave(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// --- Paint -------------------------------------------------------------------

// paintRuns is webLayout.paintRuns for a layer's own rows. strip drops the
// runs' clips, which for a pinned sticky copy were measured at its place in
// the flow. Render thread.
func (f *webFloat) paintRuns(i int, strip bool) []webPaintRun {
	if f == nil || i < 0 || i >= len(f.Rows) {
		return nil
	}
	if len(f.paint) != len(f.Rows) {
		f.paint = make([]webPaintRow, len(f.Rows))
	}
	p := &f.paint[i]
	if !p.built {
		p.runs = webBuildPaintRuns(f.Rows[i].Runs)
		if strip {
			for j := range p.runs {
				p.runs[j].run.HasClip = false
			}
		}
		p.built = true
	}
	return p.runs
}

// drawWebLayerPhase paints the layers of one pass (negative z-index layers,
// or the floats) at the page's current scroll.
func drawWebLayerPhase(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, offset int32, phase uint8) {
	for i := range layout.Floats {
		if layout.Floats[i].Phase == phase {
			drawWebFloatLayer(r, config, doc, layout, rect, &layout.Floats[i], offset)
		}
	}
}

// drawWebTopLayers paints what sits over the flow — positioned layers and
// pinned sticky bars — as one stack in stacking-key order, so a z-index: 5
// header covers a z-index: 1 badge scrolling under it and a z-index: 10
// dropdown covers the header.
func drawWebTopLayers(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, offset int32) {
	floats, stickies := layout.Floats, layout.Stickies
	i, j := 0, 0
	for {
		for i < len(floats) && floats[i].Phase != webLayerAbove {
			i++
		}
		haveF, haveS := i < len(floats), j < len(stickies)
		if !haveF && !haveS {
			return
		}
		if haveS && (!haveF || stickies[j].Key.less(floats[i].Key)) {
			drawWebStickyLayer(r, config, doc, layout, rect, &stickies[j], offset)
			j++
			continue
		}
		// A layer inside a sticky bar is drawn with the bar while it is
		// pinned (webCollectStickies), not again at its page place.
		if s := floats[i].StickyIndex; s > 0 && s <= len(stickies) {
			if _, pinned := webStickyPinnedTop(&stickies[s-1], rect, offset); pinned {
				i++
				continue
			}
		}
		drawWebFloatLayer(r, config, doc, layout, rect, &floats[i], offset)
		i++
	}
}

// webLayerTop is where a layer's top edge is on screen: at its page place
// moved by the scroll, or — pinned to the screen — at its place in the view
// whatever the scroll. view is the page's rectangle on screen.
func webLayerTop(f *webFloat, view sdl.Rect, offset int32) int32 {
	switch f.Pin {
	case webPinTop:
		return view.Y + f.Y
	case webPinBottom:
		return view.Y + view.H - f.H - f.Bottom
	case webPinCenter:
		return view.Y + (view.H-f.H)/2 + f.Y
	}
	return view.Y + f.Y - offset
}

// webPinnedHitRect is a pinned layer's hit (webLinkHit.Pinned) on screen:
// view is the page's rectangle there. ok is false for any other hit.
func webPinnedHitRect(layout *webLayout, hit webLinkHit, view sdl.Rect) (sdl.Rect, bool) {
	if layout == nil || hit.Pinned <= 0 || hit.Pinned > len(layout.Floats) {
		return sdl.Rect{}, false
	}
	top := webLayerTop(&layout.Floats[hit.Pinned-1], view, 0)
	return sdl.Rect{X: view.X + hit.Box.X, Y: top + hit.Box.Y, W: hit.Box.W, H: hit.Box.H}, true
}

// drawWebFloatLayer paints one positioned layer or float at its page place
// (or its place on the screen when pinned), inside its containing block's
// clip when it has one.
func drawWebFloatLayer(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, f *webFloat, offset int32) {
	top := webLayerTop(f, rect, offset)
	h := f.H
	if h <= 0 {
		h = int32(len(f.Rows)) * layout.RowH
	}
	if top+h <= rect.Y || top >= rect.Y+rect.H {
		return
	}
	restore := webClipPaint(r, rect, f.Clip, f.HasClip, rect.X, rect.Y-offset)
	drawWebLayer(r, config, doc, layout, rect, f, top, false)
	restore()
}

// drawWebStickyLayer paints a sticky bar's pinned copy once its place in the
// flow has scrolled past the edge it sticks to. A sticky bar keeps its flow
// rows (so it reserves space); this is the other half of CSS sticky. It
// stays inside its containing block: the bar leaves with the section, the
// table or the article it heads.
func drawWebStickyLayer(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, f *webFloat, offset int32) {
	top, pinned := webStickyPinnedTop(f, rect, offset)
	if !pinned {
		return
	}
	drawWebLayer(r, config, doc, layout, rect, f, top, true)
}

// webStickyPinnedTop is where a sticky bar's pinned copy goes on screen at
// this scroll, and whether it is pinned at all: once its place in the flow has
// scrolled past the edge it sticks to, inside its containing block, and on
// screen. A bar taller than the view never pins, nor does one taller than two
// fifths of it across most of the width (a whole column of the page); a narrow
// side column may be taller.
func webStickyPinnedTop(f *webFloat, rect sdl.Rect, offset int32) (int32, bool) {
	if f.H <= 0 || f.H > rect.H || (float64(f.H) > float64(rect.H)*webMaxStickyShare && f.W*20 > rect.W*9) {
		return 0, false
	}
	var top int32
	if f.HasBottom {
		top = rect.Y + rect.H - f.H - f.Bottom
		if f.Limit > 0 {
			top = max(top, rect.Y+f.LimitTop-offset)
		}
		if rect.Y+f.Y-offset <= top {
			// Its own place is on screen above the bottom edge.
			return 0, false
		}
	} else {
		if offset <= f.Y {
			// Still at or below its flow place, which the page draws.
			return 0, false
		}
		top = rect.Y + f.Top
		if f.Limit > 0 {
			top = min(top, rect.Y+f.Limit-offset-f.H)
		}
	}
	if top+f.H <= rect.Y || top >= rect.Y+rect.H {
		return 0, false
	}
	return top, true
}

// --- Hit testing -------------------------------------------------------------

// collectWebLayerHits turns the links and form controls inside layers and
// floats (a positioned card's button, a floated infobox's links) into cursor
// stops. A layer's rows are not page rows, so each hit carries its painted
// box and the page row it sits on, and is marked Layer so a tap prefers it to
// whatever flows underneath. A pinned layer's hit is Pinned: its box is in
// the layer's own coordinates and goes where the layer is drawn
// (webPinnedHitRect), and it is kept even when the page repeats the link —
// it is the copy that is always on screen. seen holds the destinations
// already reachable.
func collectWebLayerHits(layout *webLayout, seen map[string]bool) []webLinkHit {
	if layout == nil || layout.RowH <= 0 || len(layout.Rows) == 0 || len(layout.Floats) == 0 || seen == nil {
		return nil
	}
	lastRow := len(layout.Rows) - 1
	var hits []webLinkHit
	for fi := range layout.Floats {
		f := &layout.Floats[fi]
		if f.Lift {
			// A lifted part of the flow: the page's rows hold its links.
			continue
		}
		pinned := 0
		if f.Pin != webPinNone {
			pinned = fi + 1
		}
		for _, row := range f.Rows {
			for i := 0; i < len(row.Runs); {
				run := row.Runs[i]
				link := run.Control == nil && webHrefFollowable(run.Href)
				if run.Font == nil || run.Invisible || (!link && run.Control == nil) {
					i++
					continue
				}
				startX, endX, h := run.X, run.X, int32(0)
				label := ""
				j := i
				for ; j < len(row.Runs); j++ {
					cur := row.Runs[j]
					if cur.Font == nil || cur.Control != run.Control || (link && cur.Href != run.Href) {
						break
					}
					w, ch := cur.ImageW, cur.ImageH
					if cur.Image == nil {
						tw, th, err := cur.Font.SizeUTF8(cur.Text)
						if err != nil {
							break
						}
						w, ch = int32(tw), int32(th)
						label += " " + cur.Text
					} else if cur.Image.ImageAlt != "" {
						label += " " + cur.Image.ImageAlt
					}
					endX = max(endX, cur.X+w)
					h = max(h, ch+cur.VerticalOffset)
				}
				if j == i {
					j = i + 1
				}
				i = j
				if endX <= startX {
					continue
				}
				pageY := f.Y + row.Y
				boxY := pageY
				if pinned > 0 {
					boxY = row.Y
				}
				box := sdl.Rect{X: f.X + startX, Y: boxY, W: endX - startX, H: max(h, layout.RowH-4)}
				if !link && run.BoxW > 0 && run.BoxH > 0 {
					box = sdl.Rect{X: f.X + run.X, Y: boxY, W: run.BoxW, H: run.BoxH}
				}
				if f.HasClip && !webRectsOverlap(box, f.Clip) {
					continue
				}
				if link && pinned == 0 {
					if seen[run.Href] {
						continue
					}
					seen[run.Href] = true
				}
				// A pinned bar's stops sort with the top of the page, or the
				// end of it for a bar on the bottom edge.
				rowIdx := min(max(int(pageY/layout.RowH), 0), lastRow)
				if f.Pin == webPinBottom {
					rowIdx = lastRow
				}
				hit := webLinkHit{
					Row: rowIdx,
					X:   box.X, W: box.W, Box: box,
					Text:  strings.Join(strings.Fields(label), " "),
					Layer: true, Pinned: pinned,
				}
				if link {
					hit.Href = run.Href
				} else {
					hit.Control = run.Control
				}
				hits = append(hits, hit)
			}
		}
	}
	return hits
}
