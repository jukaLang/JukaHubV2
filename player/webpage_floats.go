package main

// webpage_floats.go — CSS floats in the reader's flow.
//
// A float used to be one block: an element's own text, or a picture. A
// floated element that held other elements — Wikipedia's thumbnail figure (a
// picture and its caption), its infobox table, a WordPress caption box, the
// columns of a float grid, a sidebar — did not float at all: its children were
// laid out in the flow at the column's full width. And a block beside a float
// was narrowed as a whole from its first row, so a long paragraph next to a
// small picture stayed narrow all the way down, and a column with a margin of
// its own beside a sidebar float was pushed across twice.
//
// Now:
//   - A floated block element opens a float root (webDocument.FloatRoots) and
//     every block inside it carries its FloatGroup. A layout pass sets the
//     group aside as one block where it starts (gatherFloatGroups) and lays it
//     out at its own width, beside the floats already there
//     (layoutFloatGroup).
//   - Plain text wraps round floats line by line (webFitLine): a line beside a
//     float is shortened by exactly the part the float's margin box covers, a
//     line below it has the whole measure again, and a line with no room left
//     goes below the float.
//   - A box that cannot run under a float — one with a surface of its own, a
//     picture, a flex row, a grid, a table — sits beside it, or below it when
//     there is no room (webBesideFloats).
//   - Floats stack: a second right float goes left of the first, and one that
//     does not fit beside the others drops below them (webFloatPlace).
//   - A clearfix (`::after { clear: both }`), an element that holds its floats
//     (overflow other than visible, display: flow-root) and the end of every
//     pass (the page, a flex or grid item, a table cell, a layer, a float)
//     contain their floats.

import (
	"math"
	"strings"
)

// webMaxFloatRoots bounds the floated subtrees one page opens. Past it a
// floated element's own text floats and its children stay in the flow, as
// they always did.
const webMaxFloatRoots = 600

// webNoFloatEdge stands for "no float on this side" (webFloatEdgesAt): far
// past any page, and far from int32's limits.
const webNoFloatEdge int32 = 1 << 28

// webFloatRootInfo is one floated subtree as the parser saw its root open: the
// root's style (its side, width, margins, clear), how many ancestor boxes lie
// outside it, how many flex and grid containers the page had opened by then
// (the ones opened later are inside it), its own box and its place in the
// document.
type webFloatRootInfo struct {
	style      webStyle
	outerBoxes int
	flexSeq    int
	rootBox    int
	seq        int
}

// --- Parser ------------------------------------------------------------------

// openFloatIn returns the floated subtree the first n open elements put new
// blocks in: the outermost float root among them, or 0.
func (p *webParser) openFloatIn(n int) int {
	for i := 0; i < n && i < len(p.stack); i++ {
		if p.stack[i].floatRoot != 0 {
			return p.stack[i].floatRoot
		}
	}
	return 0
}

// floatRootWanted reports whether a block element about to open floats with
// its whole subtree: it floats, it is not positioned out of the flow, it is no
// table part (a table itself may float), and it is not inside another floated
// subtree or a table cell, whose layout keeps a nested float to its own text,
// as before.
func (p *webParser) floatRootWanted(st *webStyle) bool {
	if st.FloatSide == 0 || st.Position == webPositionAbsolute || st.Position == webPositionFixed {
		return false
	}
	if st.TableRole != webTableRoleNone && st.TableRole != webTableRoleTable {
		return false
	}
	if len(p.doc.FloatRoots) >= webMaxFloatRoots || p.openFloatIn(len(p.stack)) != 0 {
		return false
	}
	for i := range p.stack {
		if p.stack[i].style.TableRole == webTableRoleCell {
			return false
		}
	}
	return true
}

// newFloatRoot records a floated subtree whose root is about to be pushed with
// box boxID and sequence seq. The root is not on the stack yet, so the stack
// is exactly its ancestors.
func (p *webParser) newFloatRoot(style webStyle, boxID, seq int) int {
	p.doc.FloatRoots = append(p.doc.FloatRoots, webFloatRootInfo{
		style: style, outerBoxes: len(p.boxPath()), flexSeq: p.flexSeq, rootBox: boxID, seq: seq,
	})
	return len(p.doc.FloatRoots)
}

// endFloatScope runs as the block element at stack index i closes. A
// clearfix's ::after (clearAfter is its clear side) leaves a marker that moves
// what follows below the floats; an element that holds its floats (overflow:
// hidden, display: flow-root) leaves one that moves it below the floats opened
// inside it, so a media object's picture no longer pushes the next item
// across. Neither matters on a page with no float before it.
func (p *webParser) endFloatScope(i, clearAfter int) {
	if i < 0 || i >= len(p.stack) || p.floatsSeen == 0 {
		return
	}
	f := &p.stack[i]
	switch {
	case clearAfter != 0:
		p.clearMarker(clearAfter, 0, i+1)
	case p.floatsSeen > f.floatsAt && (f.style.OverflowBFC || f.style.FlowRoot):
		p.clearMarker(3, f.seq, i+1)
	}
}

// clearMarker leaves an empty block inside the first n open elements that
// moves the flow below the floats on its clear side — those opened after
// document place from, when from is set (webClearMarkerRow).
func (p *webParser) clearMarker(clear, from, n int) {
	if clear == 0 || n <= 0 || n > len(p.stack) || len(p.blocks) >= webMaxBlocks {
		return
	}
	frame := &p.stack[n-1]
	if frame.style.TableRole != webTableRoleNone {
		// Inside a table an empty block could read as a cell of its own.
		return
	}
	st := frame.style
	webResetElementStyle(&st)
	st.Invisible = true
	st.Href, st.Control = "", nil
	st.Clear = clear
	marker := webBlock{Style: st, ClearMarker: true, ClearFrom: from, TreeSeq: frame.seq}
	for k := 0; k < n; k++ {
		if id := p.stack[k].containerID; id != 0 {
			marker.ContainerIDs = append(marker.ContainerIDs, id)
		}
		if id := p.stack[k].boxID; id != 0 {
			marker.Boxes = append(marker.Boxes, id)
		}
	}
	p.tagFlexBlock(&marker, st)
	marker.Details, marker.DetailsShown = p.detailsTag()
	marker.Layer = p.openLayerIn(n)
	marker.Float = marker.Layer != 0
	marker.FloatGroup = p.openFloatIn(n)
	p.blocks = append(p.blocks, marker)
}

// webClearfixAfter is the clear side of an element's ::after box — the
// clearfix idiom, `content: ""; display: table; clear: both` — or 0 when it
// clears nothing or is no box at all (content: none, positioned out of flow).
func webClearfixAfter(st *webStyle) int {
	if st == nil || st.Clear == 0 || st.Position == webPositionAbsolute || st.Position == webPositionFixed ||
		strings.EqualFold(strings.TrimSpace(st.Content), "none") {
		return 0
	}
	return st.Clear
}

// webBreakClear is the clear side of a <br>: its CSS clear, or the old clear
// attribute (all, both, left, right).
func webBreakClear(clear int, attrs string) int {
	if clear != 0 {
		return clear
	}
	switch strings.ToLower(strings.TrimSpace(htmlAttrValue(htmlTagAttribute(attrs, "clear")))) {
	case "all", "both":
		return 3
	case "left":
		return 1
	case "right":
		return 2
	}
	return 0
}

// webOverflowHoldsFloats reports an overflow value that makes the element a
// block formatting context: it holds the floats inside it.
func webOverflowHoldsFloats(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "hidden", "auto", "scroll", "overlay":
		return true
	}
	return false
}

// --- Layout ------------------------------------------------------------------

// webFloatEdgesAt is the room the floats leave across row: the right edge of
// the left floats' margin boxes there and the left edge of the right ones', in
// layout pixels; -webNoFloatEdge / webNoFloatEdge on a side with none.
func webFloatEdgesAt(reserves []webFloatReserve, row int32) (left, right int32) {
	left, right = -webNoFloatEdge, webNoFloatEdge
	for _, r := range reserves {
		if row < r.startRow || row >= r.endRow {
			continue
		}
		switch r.side {
		case 1:
			left = max(left, r.x1)
		case 2:
			right = min(right, r.x0)
		}
	}
	return left, right
}

// webFloatNextEnd is the row where the first of the floats beside row ends;
// row itself when none is beside it.
func webFloatNextEnd(reserves []webFloatReserve, row int32) int32 {
	next := row
	for _, r := range reserves {
		if row < r.startRow || row >= r.endRow {
			continue
		}
		if next == row || r.endRow < next {
			next = r.endRow
		}
	}
	return next
}

// webFloatsEndAt is the row by which every float beside row has ended.
func webFloatsEndAt(reserves []webFloatReserve, row int32) int32 {
	end := row
	for _, r := range reserves {
		if row >= r.startRow && row < r.endRow && r.endRow > end {
			end = r.endRow
		}
	}
	return end
}

// webFloatsEnd is the row by which every float of a pass has ended.
func webFloatsEnd(reserves []webFloatReserve) int32 {
	end := int32(0)
	for _, r := range reserves {
		end = max(end, r.endRow)
	}
	return end
}

// webClearMarkerRow is where a clear marker moves the flow from row: below
// every float on its clear side that reaches past row — of those opened inside
// the element at document place from, when from is set (an element that holds
// its own floats; a float in its own text has from as its place too).
func webClearMarkerRow(reserves []webFloatReserve, row int32, clear, from int) int32 {
	target := row
	for _, r := range reserves {
		if r.endRow <= target || (from > 0 && r.seq < from) {
			continue
		}
		if clear == 3 || clear == r.side {
			target = r.endRow
		}
	}
	return target
}

// webFloatPlace finds where a float outer pixels wide (its margin box) goes
// from row down, in the column padX..padX+contentW: the first row where the
// floats already there leave it room — or have none beside it — and the room's
// edges there. A float that fits beside none of them drops below them, as in
// CSS; two pixels of slack keep rounded percentage columns (3 × 33.33%) on one
// row.
func webFloatPlace(reserves []webFloatReserve, row, padX, contentW, outer int32) (int32, int32, int32) {
	edges := func(r int32) (int32, int32) {
		l, rt := webFloatEdgesAt(reserves, r)
		return max(padX, l), min(padX+contentW, rt)
	}
	for tries := 0; tries < 32; tries++ {
		left, right := edges(row)
		if right-left+2 >= outer || (left <= padX && right >= padX+contentW) {
			return row, left, right
		}
		next := webFloatNextEnd(reserves, row)
		if next <= row {
			return row, left, right
		}
		row = next
	}
	left, right := edges(row)
	return row, left, right
}

// webBesideFloats places a box that cannot run under a float — a surface, a
// picture, a flex row, a table — beside the floats at row. colX/colW is the
// column it is in and x/w its own place. A box that keeps its width (a table)
// moves clear of the floats when their room holds it; any other narrows to the
// room left, when that is enough to be worth it (a third of the box or 10 em).
// ok is false when it is not: the box goes below the floats.
func webBesideFloats(reserves []webFloatReserve, row, colX, colW, x, w int32, keepWidth bool, baseSize float64) (int32, int32, bool) {
	l, r := webFloatEdgesAt(reserves, row)
	if l <= x && r >= x+w {
		return x, w, true
	}
	if keepWidth {
		left, right := max(colX, l), min(colX+colW, r)
		if right-left < w {
			return x, w, false
		}
		return min(max(x, left), right-w), w, true
	}
	left, right := max(x, l), min(x+w, r)
	need := min(w, max(w/3, int32(10*baseSize)))
	if room := right - left; room < need || room < 24 {
		return x, w, false
	}
	return left, right - left, true
}

// webFitLine is one line's room beside the floats from row down. The block's
// content box starts at boxLeft and its words at start, with avail pixels to
// run; the line is shortened by exactly what the floats' margin boxes cover at
// its row, and a line below them has the whole measure. When the room left is
// too narrow to set words in (a third of the measure, or 8 em), the line goes
// down to the row where the first of those floats ends: wait is how many rows
// down. shift is how far its start moved, which the list marker follows.
func webFitLine(reserves []webFloatReserve, row, boxLeft, start, avail int32, baseSize float64) (int32, int32, int32, int32) {
	end := start + avail
	need := min(avail, max(int32(48), min(int32(8*baseSize), avail/3)))
	for r, tries := row, 0; tries < 32; tries++ {
		left, right := webFloatEdgesAt(reserves, r)
		shift := max(left-boxLeft, 0)
		cut := max(end-right, 0)
		if shift == 0 && cut == 0 {
			return start, avail, 0, r - row
		}
		if room := avail - shift - cut; room >= need {
			return start + shift, room, shift, r - row
		}
		next := webFloatNextEnd(reserves, r)
		if next <= r {
			break
		}
		r = next
	}
	return start, avail, 0, 0
}

// webBlockWrapsFloats reports a block whose lines wrap round the floats beside
// them one by one (webFitLine): plain text in the flow. A block with a surface
// of its own (background, border, outline, shadow), a picture, a rule, a
// placeholder, text that does not wrap, a control's box, a table part and a
// block that holds its floats (overflow, flow-root) is a box that sits beside
// them whole instead (webBesideFloats).
func webBlockWrapsFloats(b *webBlock) bool {
	if b.Image || b.Video || b.Rule || b.Placeholder || b.Pre || b.TableEmptyRow {
		return false
	}
	if b.HasBg || b.HasGradient || b.BoxBorder || b.BorderLeft || b.BorderRight || b.BorderTop || b.BorderBottom || b.Outline {
		return false
	}
	ws := b.WhiteSpace
	if ws == webWhiteSpaceNormal {
		ws = b.Style.WhiteSpace
	}
	st := &b.Style
	return webWhiteSpaceWraps(ws) && !st.HasBoxShadow && st.BackgroundImage == "" && st.BackgroundLayerCount == 0 &&
		!st.OverflowBFC && !st.FlowRoot && st.OverflowX == 0 && st.OverflowY == 0 &&
		st.TableRole == webTableRoleNone && st.Control == nil
}

// webBlockInFlow reports a block the flow lays out where it stands: not a
// layer, a float, a floated subtree or a marker.
func webBlockInFlow(b *webBlock) bool {
	return !b.Float && b.FloatBlocks == nil && b.Style.FloatSide == 0 && !b.ClearMarker && b.LayerMarker == 0
}

// gatherFloatGroups sets each floated subtree that starts in this pass aside
// as one block that stands for it (FloatBlocks), at its place in the flow. A
// subtree starts here once the flex and grid containers around it have been
// laid out: its blocks' current flex group is none, or one opened inside the
// float (webFloatRootInfo.flexSeq).
func (l *webLayouter) gatherFloatGroups(blocks []webBlock) []webBlock {
	if l.doc == nil || len(l.doc.FloatRoots) == 0 {
		return blocks
	}
	var out []webBlock
	for i := 0; i < len(blocks); i++ {
		b := &blocks[i]
		id := b.FloatGroup
		if id <= 0 || id > len(l.doc.FloatRoots) || (b.FlexGroup != 0 && b.FlexGroup <= l.doc.FloatRoots[id-1].flexSeq) {
			if out != nil {
				out = append(out, *b)
			}
			continue
		}
		j := i + 1
		for j < len(blocks) && blocks[j].FloatGroup == id {
			j++
		}
		if out == nil {
			out = make([]webBlock, 0, len(blocks))
			out = append(out, blocks[:i]...)
		}
		info := l.doc.FloatRoots[id-1]
		group := make([]webBlock, j-i)
		copy(group, blocks[i:j])
		// The stand-in carries the boxes around the float, which give it its
		// containing block, and nothing of its own. They come from a block
		// that has the whole chain: a table's row marker carries none.
		from := b
		for k := range group {
			if len(group[k].Boxes) >= info.outerBoxes {
				from = &group[k]
				break
			}
		}
		stand := webBlock{
			FloatGroup: id, FloatBlocks: group, TreeSeq: info.seq,
			ContainerIDs: from.ContainerIDs, Details: b.Details, DetailsShown: b.DetailsShown,
		}
		stand.Boxes = from.Boxes[:min(info.outerBoxes, len(from.Boxes))]
		// It sits in the innermost of them, so a header made only of floats
		// (a logo left, a menu right) still opens and reserves its height.
		if n := len(stand.Boxes); n > 0 {
			stand.BoxOwner = stand.Boxes[n-1]
		}
		out = append(out, stand)
		i = j - 1
	}
	if out == nil {
		return blocks
	}
	return out
}

// layoutFloatGroup lays out one floated subtree — a figure with its caption,
// an infobox table, a column of a float grid — at its own width and parks it
// in the layer list like any float: at row, at its side of the containing
// block padX..padX+contentW, beside the floats already there (reserves) or
// below them when it does not fit. It returns the room it takes, which the
// flow wraps round.
func (l *webLayouter) layoutFloatGroup(g webBlock, padX, contentW, row int32, reserves []webFloatReserve) (webFloatReserve, bool) {
	if l.doc == nil || g.FloatGroup <= 0 || g.FloatGroup > len(l.doc.FloatRoots) || len(g.FloatBlocks) == 0 ||
		contentW <= 0 || l.rowH <= 0 || l.baseSize <= 0 {
		return webFloatReserve{}, false
	}
	info := l.doc.FloatRoots[g.FloatGroup-1]
	st := info.style
	scale := l.baseSize / 16
	px := func(css float64) int32 { return int32(math.Round(css * scale)) }
	// The root's margins are outside its box and keep the text off it (CSS
	// px, not below zero); its padding is inside the box.
	cssML, cssMR := max(st.Indent-st.PadLeft, 0), max(st.IndentRight-st.PadRight, 0)
	marginL, marginR := px(cssML), px(cssMR)
	pads := max(px(st.PadLeft), 0) + max(px(st.PadRight), 0)
	if st.Clear != 0 {
		row = max(row, webClearRow(reserves, row, st.Clear))
	}
	room := max(contentW-marginL-marginR, 1)
	// The border box: the root's own width, or as wide as what it holds.
	var w int32
	switch {
	case st.WidthPct > 0:
		w = int32(float64(contentW) * st.WidthPct / 100)
		if st.BoxSizing != 1 {
			w += pads
		}
	case st.Width > 0:
		w = px(st.Width)
		if st.BoxSizing != 1 {
			w += pads
		}
	default:
		w = l.floatGroupWidth(g.FloatBlocks, info, max(room-pads, 1)) + pads
	}
	if maxW := webConstraintLength(st.MaxWidth, st.MaxWidthPct, contentW, scale); maxW > 0 {
		limit := int32(maxW * scale)
		if st.BoxSizing != 1 {
			limit += pads
		}
		w = min(w, limit)
	}
	if minW := webConstraintLength(st.MinWidth, st.MinWidthPct, contentW, scale); minW > 0 {
		floor := int32(minW * scale)
		if st.BoxSizing != 1 {
			floor += pads
		}
		w = max(w, floor)
	}
	w = max(min(w, room), min(room, 16))

	row, left, right := webFloatPlace(reserves, row, padX, contentW, w+marginL+marginR)
	if fit := right - left - marginL - marginR; w > fit && fit >= 16 {
		w = fit
	}
	x := left + marginL
	if st.FloatSide == 2 {
		x = max(right-marginR-w, left+marginL)
	}

	// The subtree is laid out at that width as flow of its own. The root's
	// width and margins were spent above, so its box keeps only its padding.
	blocks := make([]webBlock, len(g.FloatBlocks))
	copy(blocks, g.FloatBlocks)
	for i := range blocks {
		b := &blocks[i]
		b.FloatGroup = 0
		if b.TreeSeq != info.seq || b.Image {
			continue
		}
		b.Width, b.WidthPct = 0, 0
		b.Style.Width, b.Style.WidthPct = 0, 0
		b.Style.MinWidth, b.Style.MaxWidth = 0, 0
		b.Style.MinWidthPct, b.Style.MaxWidthPct = 0, 0
		b.Style.MarginLeftAuto, b.Style.MarginRightAuto = false, false
		b.Style.FloatSide, b.Style.Clear = 0, 0
		b.Indent = max(b.Indent-cssML, 0)
		b.Style.Indent = max(b.Style.Indent-cssML, 0)
		b.Style.IndentRight = max(b.Style.IndentRight-cssMR, 0)
	}
	sub := &webLayout{RowH: l.rowH}
	sl := &webLayouter{
		doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
		spaceWidths: l.spaceWidths, sanitized: l.sanitized,
		containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan),
		constraintSkip: info.outerBoxes,
	}
	if root, ok := l.doc.Boxes[info.rootBox]; ok && info.rootBox != 0 {
		root.style.Width, root.style.WidthPct = 0, 0
		root.style.MinWidth, root.style.MaxWidth = 0, 0
		root.style.MinWidthPct, root.style.MaxWidthPct = 0, 0
		root.style.MarginLeftAuto, root.style.MarginRightAuto = false, false
		root.style.Indent = max(root.style.Indent-cssML, 0)
		root.style.IndentRight = max(root.style.IndentRight-cssMR, 0)
		sl.boxOverride = map[int]webBoxInfo{info.rootBox: root}
	}
	sl.layoutBlocks(blocks, 0, w)
	l.absorbContainerRows(sl.containerRows)
	if len(sub.Rows) == 0 && len(sub.Rects) == 0 && len(sub.Images) == 0 && len(sub.Backgrounds) == 0 &&
		len(sub.Videos) == 0 && len(sub.Floats) == 0 {
		return webFloatReserve{}, false
	}
	webSortPaintOrder(sub)
	h := max(webLayerExtent(sub), sub.PaintBottom)
	if floor := px(max(st.Height, st.MinHeight)); floor > 0 {
		if st.BoxSizing != 1 {
			floor += px(st.PadTop + st.PadBot)
		}
		h = max(h, floor)
	}
	// The root's bottom padding and margin come after its content.
	h = max(h+max(px(st.MarginBot), 0), l.rowH)
	y := row * l.rowH
	// A float paints over the flow's surfaces and under its text and every
	// positioned layer (webpage_layers.go); its box's own surface is painted
	// with the flow's, from its runs (paintContainerBoxes).
	f := webFloat{
		Z: st.ZIndex, X: x, Y: y, H: h, W: w,
		Phase: webLayerFloat, Key: webFreeStackKey(st.ZIndex, info.seq), Root: info.rootBox,
		Rows: sub.Rows, Rects: sub.Rects, Backgrounds: sub.Backgrounds,
		Images: sub.Images, Videos: sub.Videos,
	}
	l.layout.Floats = append(l.layout.Floats, f)
	// The floats inside it move with it.
	for _, sf := range sub.Floats {
		sf.X += x
		sf.Y += y
		l.layout.Floats = append(l.layout.Floats, sf)
	}
	l.mergeStatics(sub, x, y)
	for id, sh := range sub.boxStretch {
		l.stretchBox(id, sh)
	}
	l.mergeBoxGeom(sub, x)
	return webFloatReserve{
		side: st.FloatSide, startRow: row, endRow: row + (h+l.rowH-1)/l.rowH,
		x0: x - marginL, x1: x + w + marginR, seq: info.seq,
	}, true
}

// floatGroupWidth is how wide a floated subtree with no width of its own is
// (CSS's shrink-to-fit), its content only, in layout pixels within room: its
// widest picture, line or row. A caption (display: table-caption, as
// Wikipedia's figures have it) wraps to that width instead of widening it, a
// box inside with a width of its own (a thumbnail's frame) is as wide as that,
// a flex row inside is as wide as its items together, and a table as its
// widest row.
func (l *webLayouter) floatGroupWidth(blocks []webBlock, info webFloatRootInfo, room int32) int32 {
	if room <= 0 || l.baseSize <= 0 {
		return 0
	}
	widest, caption := int32(0), int32(0)
	cells := make(map[[2]int]int32)
	items := make(map[[2]int]int32)
	gaps := make(map[int]int32)
	// floatRun is the width of the floats inside this one that sit side by
	// side so far: the items of a floated menu (`ul.left { float: left }`,
	// `li { float: left }`) line up in a row, so the menu is as wide as all
	// of them, not as its widest — they used to drop one under another.
	floatRun := int32(0)
	for i := range blocks {
		b := blocks[i]
		if b.LayerMarker != 0 || b.ClearMarker || b.TableEmptyRow || b.Float {
			continue
		}
		if b.Style.FloatSide != 0 && b.TreeSeq != info.seq && !b.Image && b.Style.TableRole == webTableRoleNone {
			if b.Style.Clear != 0 {
				floatRun = 0
			}
			// As wide as layoutCSSFloat will make it.
			if w := l.intrinsicContainerWidth([]webBlock{b}); w > 0 {
				floatRun += w
			} else {
				floatRun += int32(math.Round(120 * l.baseSize / 16))
			}
			widest = max(widest, floatRun)
			continue
		}
		floatRun = 0
		if fixed := l.floatInnerBoxWidth(&b, info); fixed > 0 {
			widest = max(widest, fixed)
			continue
		}
		if b.TreeSeq == info.seq && !b.Image {
			// The root's own text: the caller adds the root's padding.
			b.Indent, b.Style.IndentRight = 0, 0
		}
		var w int32
		switch {
		case b.Style.TableRole == webTableRoleCaption:
			caption = max(caption, l.intrinsicMinContentWidth([]webBlock{b}))
			continue
		case b.Image:
			if w = l.floatPictureWidth(&b, room); w <= 0 {
				w = room / 2
			}
		default:
			probe := b
			probe.Style.TableRole = webTableRoleNone
			probe.FlexTrail = nil
			if w = l.shrinkToFitWidth([]webBlock{probe}, room); w <= 0 {
				w = l.intrinsicContainerWidth([]webBlock{b})
			}
		}
		switch {
		case b.Style.TableRole == webTableRoleCell:
			// A table is as wide as its cells side by side.
			cells[[2]int{b.Style.TableID, b.Style.TableRow}] += w
		case b.FlexGroup != 0 && b.FlexDir == webFlexRow:
			// So is a flex row of its items.
			key := [2]int{b.FlexGroup, b.FlexItem}
			if _, ok := items[key]; !ok && b.FlexGapX > 0 {
				gaps[b.FlexGroup] += int32(math.Round(b.FlexGapX * l.baseSize / 16))
			}
			items[key] = max(items[key], w)
		default:
			widest = max(widest, w)
		}
	}
	for _, w := range cells {
		widest = max(widest, w)
	}
	rows := make(map[int]int32)
	for key, w := range items {
		rows[key[0]] += w
	}
	for group, w := range rows {
		widest = max(widest, w+gaps[group])
	}
	widest = max(widest, caption)
	if widest <= 0 || widest > room {
		return room
	}
	return widest
}

// floatPictureWidth is the width of a floated picture of known size, in layout
// pixels within the column contentW: its CSS width (a percentage of the
// column too) or width attribute, its height with a known shape, or the
// file's own width, held to its max-width — `max-width: 100%`, which nearly
// every theme gives its images, used to make a floated photo as wide as the
// column with no room for text beside it. 0 when the size is not known yet.
func (l *webLayouter) floatPictureWidth(b *webBlock, contentW int32) int32 {
	if b == nil || contentW <= 0 || l.baseSize <= 0 {
		return 0
	}
	known := b.Style.WidthPct > 0 || b.Style.Width > 0 || b.ImageW > 0
	if !known && l.doc != nil {
		if meta, ok := l.doc.imageMeta(b.ImageSrc); ok && meta.HasSize {
			known = true
		}
	}
	if !known {
		return 0
	}
	scale := l.baseSize / 16
	limit := contentW
	if b.Style.MaxWidthPct > 0 {
		limit = min(limit, int32(float64(contentW)*b.Style.MaxWidthPct/100))
	} else if b.Style.MaxWidth > 0 {
		limit = min(limit, int32(math.Round(b.Style.MaxWidth*scale)))
	}
	if limit <= 0 {
		return 0
	}
	w, _ := webImageDimsFromBlock(*b, l.doc, scale, limit)
	return w
}

// floatInnerBoxWidth is the width of the outermost box inside a floated
// subtree's root that has a width of its own and holds b — a thumbnail's
// `width: 222px` frame round its picture and caption — as a border box in
// layout pixels; 0 when there is none.
func (l *webLayouter) floatInnerBoxWidth(b *webBlock, info webFloatRootInfo) int32 {
	from := info.outerBoxes
	if info.rootBox != 0 {
		from++
	}
	scale := l.baseSize / 16
	for k := from; k < len(b.Boxes); k++ {
		box, ok := l.doc.Boxes[b.Boxes[k]]
		if !ok || box.style.Width <= 0 {
			continue
		}
		w := box.style.Width * scale
		if box.style.BoxSizing != 1 {
			w += (box.style.PadLeft + box.style.PadRight) * scale
		}
		return int32(math.Round(w))
	}
	return 0
}
