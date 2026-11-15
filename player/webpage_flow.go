package main

// Flexbox rows and stacked (position: absolute / z-index) blocks.
//
// The reader has no DOM and no CSS engine: the parser tags every direct child
// of a flex container with the container it belongs to, and the passes here
// turn those tags into geometry on the same uniform row grid the rest of the
// page uses. A flex item is laid out by its own small webLayouter and then
// merged into the page at an offset, which keeps wrapping, bullets and inline
// styles working inside a card exactly as they do in the flow.

import (
	"math"
	"sort"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// flexCell is one measured item of a flex row, held until every sibling is
// measured so cross-axis alignment can be resolved.
type flexCell struct {
	sub   *webLayout
	w     int32
	align int
}

type webFlexItem struct {
	blocks []webBlock
}

// flexAutoMain reports the auto margins on a flex item's main axis, in
// main-start/main-end order. Physical margins are mirrored for reverse
// directions, which is how `margin-left: auto` becomes an end margin in a
// row-reverse container.
func flexAutoMain(b webBlock) (start, end bool) {
	if b.FlexDir == webFlexColumn {
		if b.FlexReverse {
			return b.Style.MarginBotAuto, b.Style.MarginTopAuto
		}
		return b.Style.MarginTopAuto, b.Style.MarginBotAuto
	}
	if b.FlexReverse {
		return b.Style.MarginRightAuto, b.Style.MarginLeftAuto
	}
	return b.Style.MarginLeftAuto, b.Style.MarginRightAuto
}

// flexAutoCross reports auto margins on the cross axis. Rows use the block
// axis, columns the inline axis; reverse directions do not swap the cross
// edges of a row.
func flexAutoCross(b webBlock) (start, end bool) {
	if b.FlexDir == webFlexColumn {
		return b.Style.MarginLeftAuto, b.Style.MarginRightAuto
	}
	return b.Style.MarginTopAuto, b.Style.MarginBotAuto
}

// webFlexContainerHeight returns the definite block size a flex column can
// distribute. A percentage height has no resolved parent height in this
// reader, so it stays content-sized; a min-height is a definite floor and can
// be used as the container's block size.
func webFlexContainerHeight(st webStyle) float64 {
	if st.Height > 0 {
		return st.Height
	}
	return st.MinHeight
}

func flexAutoCount(items []webFlexItem) int {
	count := 0
	for _, item := range items {
		start, end := flexAutoMain(item.blocks[0])
		if start {
			count++
		}
		if end {
			count++
		}
	}
	return count
}

// flexAutoSpaces splits free main-axis space over the auto edges. The row and
// column paths share this so a column margin-top:auto behaves exactly like a
// row margin-left:auto, including the both-auto centre case.
func flexAutoSpaces(items []webFlexItem, free int32) (startSpace, endSpace []int32) {
	startSpace = make([]int32, len(items))
	endSpace = make([]int32, len(items))
	if free <= 0 {
		return startSpace, endSpace
	}
	count := flexAutoCount(items)
	if count == 0 {
		return startSpace, endSpace
	}
	remaining := free
	remainingSlots := int32(count)
	for i, item := range items {
		start, end := flexAutoMain(item.blocks[0])
		if start {
			startSpace[i] = remaining / remainingSlots
			remaining -= startSpace[i]
			remainingSlots--
		}
		if end {
			endSpace[i] = remaining / remainingSlots
			remaining -= endSpace[i]
			remainingSlots--
		}
	}
	return startSpace, endSpace
}

// flexCrossOffset resolves a column flex item's horizontal auto margins
// against the column's content width. It uses the same constrained-width
// calculation as block flow, so a fixed-width card centres and a full-width
// one keeps the whole line.
func (l *webLayouter) flexCrossOffset(blocks []webBlock, contentW int32) int32 {
	if len(blocks) == 0 || contentW <= 0 || l.baseSize <= 0 {
		return 0
	}
	b := blocks[0]
	start, end := flexAutoCross(b)
	if !start && !end {
		return 0
	}
	scale := l.baseSize / 16
	width := webBlockContentWidth(b, contentW, l.baseSize)
	free := contentW - width - int32((b.Indent+b.Style.IndentRight)*scale)
	if free <= 0 {
		return 0
	}
	if start && end {
		return free / 2
	}
	if start {
		return free
	}
	return 0
}

func (l *webLayouter) flexColumnHeightRows(height float64) int32 {
	if height <= 0 || l.baseSize <= 0 || l.rowH <= 0 {
		return 0
	}
	rows := int32(math.Ceil(height * l.baseSize / 16 / float64(l.rowH)))
	if rows < 1 {
		rows = 1
	}
	return rows
}

// mergeFlexSubLayout moves one measured item into the page row grid. The item
// is measured at x=0, so its painted boxes and runs move together here.
func (l *webLayouter) mergeFlexSubLayout(sub *webLayout, x, rowOffset int32) {
	if sub == nil {
		return
	}
	ensureRows := func(last int32) {
		for int32(len(l.layout.Rows)) <= last && len(l.layout.Rows) < webMaxRows {
			idx := int32(len(l.layout.Rows))
			l.layout.Rows = append(l.layout.Rows, webRow{Y: idx * l.rowH})
		}
	}
	for ri, row := range sub.Rows {
		target := rowOffset + int32(ri)
		ensureRows(target)
		if target < 0 || target >= int32(len(l.layout.Rows)) {
			continue
		}
		for _, run := range row.Runs {
			run.X += x
			l.layout.Rows[target].Runs = append(l.layout.Rows[target].Runs, run)
		}
	}
	dy := rowOffset * l.rowH
	for _, rect := range sub.Rects {
		rect.Rect.X += x
		rect.Rect.Y += dy
		l.layout.Rects = append(l.layout.Rects, rect)
	}
	for _, bg := range sub.Backgrounds {
		bg.Rect.X += x
		bg.Rect.Y += dy
		l.layout.Backgrounds = append(l.layout.Backgrounds, bg)
	}
	for _, im := range sub.Images {
		im.Rect.X += x
		im.Rect.Y += dy
		l.layout.Images = append(l.layout.Images, im)
	}
	for _, video := range sub.Videos {
		video.Rect.X += x
		video.Rect.Y += dy
		l.layout.Videos = append(l.layout.Videos, video)
	}
	for _, float := range sub.Floats {
		float.X += x
		float.Y += dy
		l.layout.Floats = append(l.layout.Floats, float)
	}
}

// layoutFlexColumnAuto is the column counterpart of the row main-axis path.
// It measures each item, hands the container's spare block rows to the same
// flexAutoSpaces allocator the row uses, and then merges the items at those
// offsets. It is only used when the container has a definite block height.
func (l *webLayouter) layoutFlexColumnAuto(grouped []webFlexItem, block webBlock, padX, contentW, containerRows int32) {
	type measuredItem struct {
		item     webFlexItem
		sub      *webLayout
		rows     int32
		before   int32
		after    int32
		gapAbove int32
	}
	gap := webMarginRows(block.FlexGapY, l.baseSize, l.rowH)
	measured := make([]measuredItem, 0, len(grouped))
	total := int32(0)
	for i, item := range grouped {
		gapAbove := int32(0)
		if i > 0 {
			gapAbove = gap + webMarginRows(item.blocks[0].MarginTop, l.baseSize, l.rowH)
		}
		blocks := item.blocks
		if i == 0 {
			// The group's leading margin belongs to the container, not to the
			// first item; the caller has already collapsed it with the flow.
			blocks = append([]webBlock(nil), item.blocks...)
			blocks[0].MarginTop = 0
		}
		sub := &webLayout{RowH: l.rowH}
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
			spaceWidths: l.spaceWidths, sanitized: l.sanitized,
			containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan)}
		subLayouter.primeItemContainerWidths(blocks, contentW)
		subLayouter.layoutBlocksFlat(blocks, 0, contentW)
		l.absorbContainerRows(subLayouter.containerRows)
		rows := int32(len(sub.Rows))
		if rows < 1 && webItemVisible(blocks[0]) {
			rows = 1
		}
		total += gapAbove + rows
		measured = append(measured, measuredItem{item: item, sub: sub, rows: rows, gapAbove: gapAbove})
	}
	free := containerRows - total
	if free < 0 {
		free = 0
	}
	startSpace, endSpace := flexAutoSpaces(grouped, free)
	baseRow := int32(len(l.layout.Rows))
	cursor := baseRow
	for i := range measured {
		m := &measured[i]
		cursor += m.gapAbove
		before, after := startSpace[i], endSpace[i]
		if block.FlexReverse {
			// flexAutoMain reports main-start/main-end. In a column-reverse
			// container the visual top is the main-end edge, so the gaps swap.
			before, after = after, before
		}
		cursor += before
		x := padX + l.flexCrossOffset(m.item.blocks, contentW)
		l.mergeFlexSubLayout(m.sub, x, cursor)
		cursor += m.rows + after
	}
	targetRows := containerRows
	if used := cursor - baseRow; used > targetRows {
		targetRows = used
	}
	for int32(len(l.layout.Rows)) < baseRow+targetRows && len(l.layout.Rows) < webMaxRows {
		idx := int32(len(l.layout.Rows))
		l.layout.Rows = append(l.layout.Rows, webRow{Y: idx * l.rowH})
	}
}

// A direct flex or grid child can contain several paragraphs. They share one
// FlexItem number and must occupy one cell, rather than becoming separate
// columns just because the child had an h2 followed by a p.
func webGroupFlexItems(blocks []webBlock) []webFlexItem {
	var grouped []webFlexItem
	last := -1
	for _, block := range blocks {
		if block.FlexItem != last || block.FlexItem == 0 {
			grouped = append(grouped, webFlexItem{})
			last = block.FlexItem
		}
		grouped[len(grouped)-1].blocks = append(grouped[len(grouped)-1].blocks, block)
	}
	visible := grouped[:0]
	for _, item := range grouped {
		for _, block := range item.blocks {
			if webItemVisible(block) {
				visible = append(visible, item)
				break
			}
		}
	}
	// `order` applies to flex and grid items, and equal values keep source
	// order. The parser groups a card's paragraphs first, then this reorders
	// whole cards rather than their individual blocks.
	sort.SliceStable(visible, func(i, j int) bool {
		return visible[i].blocks[0].Order < visible[j].blocks[0].Order
	})
	return visible
}

func webFlattenFlexItems(items []webFlexItem) []webBlock {
	var blocks []webBlock
	for _, item := range items {
		blocks = append(blocks, item.blocks...)
	}
	return blocks
}

// webItemVisible reports whether a flex item has anything worth a column. A
// wrapper element with no text of its own would otherwise claim a whole empty
// column and squeeze the content next to it.
func webItemVisible(b webBlock) bool {
	if b.Style.TableRole == webTableRoleCell {
		return true
	}
	if len(b.Runs) > 0 || b.Rule || b.Image || b.Video || b.Placeholder {
		return true
	}
	return b.HasBg || b.BoxBorder || b.BorderLeft || b.BorderTop || b.BorderBottom
}

// layoutBlocksFlat lays a run of blocks out as ordinary stacked content. It is
// the fallback for a flex row that cannot fit side-by-side columns on a small
// screen: a 20px column of wrapped text is worse than losing the row.
func (l *webLayouter) layoutBlocksFlat(blocks []webBlock, padX, contentW int32) {
	flat := make([]webBlock, len(blocks))
	copy(flat, blocks)
	if len(flat) > 0 {
		// The flex/grid parent already resolved the item root's auto margins
		// against the row or track. Leaving them set would make the ordinary
		// block pass centre the same item a second time.
		flat[0].Style.MarginLeftAuto, flat[0].Style.MarginRightAuto = false, false
		flat[0].Style.MarginTopAuto, flat[0].Style.MarginBotAuto = false, false
	}
	for i := range flat {
		if len(flat[i].FlexTrail) > 0 {
			flat[i].setFlexMembership(flat[i].FlexTrail[0])
			flat[i].FlexTrail = flat[i].FlexTrail[1:]
		} else {
			flat[i].FlexGroup = 0
			flat[i].FlexItem = 0
		}
	}
	l.layoutBlocks(flat, padX, contentW)
}

// itemWidth resolves an item's base width in pixels, or 0 when the item is
// flexible and should share whatever room is left over.
func (l *webLayouter) itemWidth(b webBlock, avail int32) int32 {
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	width := int32(0)
	// flex-basis overrides width when both are present. A zero basis stays
	// zero here and receives its share through flex-grow below.
	if b.BasisPct > 0 {
		width = int32(float64(avail) * b.BasisPct / 100)
	} else if b.FlexBasis >= 0 {
		width = int32(b.FlexBasis * scale)
	} else if b.WidthPct > 0 {
		width = int32(float64(avail) * b.WidthPct / 100)
	} else if b.Width > 0 {
		width = int32(b.Width * scale)
	}
	if b.Style.BoxSizing == 1 && width > 0 {
		width -= int32((b.Indent + b.Style.IndentRight) * scale)
	}
	if width < 0 {
		width = 0
	}
	maxW := webConstraintLength(b.Style.MaxWidth, b.Style.MaxWidthPct, avail, scale)
	minW := webConstraintLength(b.Style.MinWidth, b.Style.MinWidthPct, avail, scale)
	if width == 0 && maxW > 0 {
		width = int32(math.Round(maxW * scale))
	}
	if maxW > 0 && float64(width)/scale > maxW {
		width = int32(math.Round(maxW * scale))
	}
	if minW > 0 && float64(width)/scale < minW {
		width = int32(math.Round(minW * scale))
	}
	if width > avail {
		width = avail
	}
	if width < 0 {
		width = 0
	}
	return width
}

// layoutCSSFloat measures a `float: left/right` block at its own width and
// parks it in the layer list at the current flow row. The caller records the
// returned row span so following siblings shorten beside it (or move below it
// for clear). A float does not advance the flow itself, exactly like CSS.
func (l *webLayouter) layoutCSSFloat(block webBlock, padX, contentW, startRow, leftActive, rightActive int32) (endRow, width int32) {
	if contentW <= 0 || l.rowH <= 0 {
		return startRow, 0
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	style := block.Style
	autoWidth := style.Width <= 0 && style.WidthPct <= 0 &&
		style.MinWidth <= 0 && style.MaxWidth <= 0 && style.MinWidthPct <= 0 && style.MaxWidthPct <= 0
	if autoWidth {
		width = l.intrinsicContainerWidth([]webBlock{block})
		if width <= 0 {
			width = int32(120 * scale)
		}
		if maxW := webConstraintLength(style.MaxWidth, style.MaxWidthPct, contentW, scale); maxW > 0 && float64(width)/scale > maxW {
			width = int32(math.Round(maxW * scale))
		}
		if minW := webConstraintLength(style.MinWidth, style.MinWidthPct, contentW, scale); minW > 0 && float64(width)/scale < minW {
			width = int32(math.Round(minW * scale))
		}
	} else {
		width = webBlockContentWidth(block, contentW, l.baseSize)
	}
	maxWidth := contentW - leftActive - rightActive
	if maxWidth < 16 {
		maxWidth = contentW
	}
	if width > maxWidth {
		width = maxWidth
	}
	if width < 16 {
		width = 16
	}
	x := padX + leftActive
	if style.FloatSide == 2 {
		x = padX + contentW - width
		if x < padX+leftActive {
			x = padX + leftActive
		}
	}

	inner := block
	inner.MarginTop, inner.MarginBot = 0, 0
	inner.Float = false
	inner.Style.FloatSide = 0
	inner.Style.Clear = 0
	inner.Style.Position = webPositionStatic
	inner.Style.Width, inner.Style.WidthPct = 0, 0
	inner.Style.MinWidth, inner.Style.MaxWidth = 0, 0
	inner.Style.MinWidthPct, inner.Style.MaxWidthPct = 0, 0
	sub := &webLayout{RowH: l.rowH}
	sl := &webLayouter{
		doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
		spaceWidths:    map[*ttf.Font]int32{},
		sanitized:      make(map[webSanitizeKey]string, 64),
		containerSizes: l.containerSizes,
		containerRows:  make(map[int]webContainerRowSpan),
	}
	sl.layoutBlocks([]webBlock{inner}, 0, width)
	if len(sub.Rows) == 0 {
		return startRow, width
	}
	// The float's flow reserve covers its painted box, not just its text
	// rows: a padded float otherwise let `clear` put the next paragraph over
	// its bottom padding. min-height/height are floors here just as they are
	// in the ordinary block box.
	padTop := int32(math.Round(block.Style.PadTop * scale))
	padBot := int32(math.Round(block.Style.PadBot * scale))
	if padTop < 0 {
		padTop = 0
	}
	if padBot < 0 {
		padBot = 0
	}
	boxH := int32(len(sub.Rows))*l.rowH + padTop + padBot
	if floor := int32(math.Round(max(block.Style.Height, block.Style.MinHeight) * scale)); floor > boxH {
		boxH = floor + padTop + padBot
	}
	endRow = startRow + int32(math.Ceil(float64(boxH)/float64(l.rowH)))
	l.layout.Floats = append(l.layout.Floats, webFloat{
		Z:           block.ZIndex,
		X:           x,
		Y:           startRow * l.rowH,
		Rows:        sub.Rows,
		Rects:       sub.Rects,
		Backgrounds: sub.Backgrounds,
		Images:      sub.Images,
	})
	return endRow, width
}

// itemContainerID finds the containment container that is the flex item's own
// box. A container nested inside that box shares the same FlexItem number, so
// the search stops at the outermost one whose parent is not also in the item.
func (l *webLayouter) itemContainerID(blocks []webBlock) int {
	if l.doc == nil || len(blocks) == 0 {
		return 0
	}
	b := blocks[0]
	if b.FlexGroup == 0 || b.FlexItem == 0 {
		return 0
	}
	for _, id := range b.ContainerIDs {
		info, ok := l.doc.Containers[id]
		if !ok || info.flexGroup != b.FlexGroup || info.flexItem != b.FlexItem {
			continue
		}
		if parent, ok := l.doc.Containers[info.parent]; ok &&
			parent.flexGroup == info.flexGroup && parent.flexItem == info.flexItem {
			continue
		}
		return id
	}
	return 0
}

// intrinsicContainerWidth measures the text in a content-sized container. The
// reader has no inline formatting context to ask, so the widest run plus a
// small box padding is the closest thing to a shrink-to-fit inline size it can
// know before laying the item out.
func (l *webLayouter) intrinsicContainerWidth(blocks []webBlock) int32 {
	if l.fontAt == nil || l.baseSize <= 0 {
		return 0
	}
	scale := l.baseSize / 16
	widest := int32(0)
	for _, b := range blocks {
		padding := int32((b.Indent + b.Style.IndentRight) * scale)
		for _, run := range b.Runs {
			text := strings.TrimSpace(run.Text)
			if text == "" {
				continue
			}
			font := l.fontAt(run.Style.FontPx)
			if font == nil {
				continue
			}
			w, _, err := font.SizeUTF8(text)
			if err != nil {
				continue
			}
			if width := int32(w) + padding; width > widest {
				widest = width
			}
		}
	}
	if widest <= 0 {
		return 0
	}
	// A little breathing room keeps the first word from touching the edge and
	// gives cq* margins inside the box a real percentage to resolve against.
	return widest + int32(24*scale)
}

// setContainerWidth overwrites one measured container width and resolves its
// nested containers from it. This is how a flex track makes cqw/cqi agree with
// the column the reader actually drew.
func (l *webLayouter) setContainerWidth(id int, width int32) {
	if id == 0 || l.doc == nil || l.containerSizes == nil || l.baseSize <= 0 {
		return
	}
	cssWidth := float64(width) * 16 / l.baseSize
	l.applyContainerWidth(id, cssWidth)
}

func (l *webLayouter) applyContainerWidth(id int, width float64) {
	if _, ok := l.doc.Containers[id]; !ok {
		return
	}
	size := l.containerSizes[id]
	size.Width = width
	l.containerSizes[id] = size
	for childID, child := range l.doc.Containers {
		if child.parent != id {
			continue
		}
		childWidth := width
		switch {
		case child.widthPct > 0:
			childWidth = width * child.widthPct / 100
		case child.width > 0:
			childWidth = child.width
		}
		l.applyContainerWidth(childID, childWidth)
	}
}

// primeItemContainerWidths makes a sub-layout's own column width the used size
// of the container at the top of that item before its blocks are measured.
func (l *webLayouter) primeItemContainerWidths(blocks []webBlock, width int32) {
	if id := l.itemContainerID(blocks); id != 0 {
		l.setContainerWidth(id, width)
	}
}

// containerBoxSpan is the union of the runs that carry one container-box ID.
// It is collected after the page is laid out so a box that contains only
// child blocks still has real geometry.
type containerBoxSpan struct {
	minX, minY int32
	maxX, maxY int32
	width      int32
	seen       bool
}

// paintContainerBoxes paints the backgrounds, borders and shadows of elements
// that produced no block of their own. Cards, sidebars and hero sections used
// to lay out their children correctly and then lose the box that gave those
// children their visible surface. Each run carries the box IDs of its
// ancestors; a union of the runs is the box's used rectangle.
func (l *webLayouter) paintContainerBoxes() {
	if l == nil || l.layout == nil || l.doc == nil || len(l.doc.Boxes) == 0 {
		return
	}
	spans := make(map[int]*containerBoxSpan)
	addRun := func(run webRunPos, rowY, ox, oy int32) {
		if len(run.Boxes) == 0 || run.Font == nil {
			return
		}
		w, h, err := run.Font.SizeUTF8(run.Text)
		if err != nil {
			return
		}
		y := rowY + oy
		for _, id := range run.Boxes {
			span := spans[id]
			if span == nil {
				span = &containerBoxSpan{minX: run.X + ox, minY: y, width: run.BoxWidth}
				spans[id] = span
			}
			if !span.seen {
				span.minX, span.minY, span.seen = run.X+ox, y, true
			}
			if x := run.X + ox; x < span.minX {
				span.minX = x
			}
			if x := run.X + ox + int32(w); x > span.maxX {
				span.maxX = x
			}
			if y2 := y + int32(h); y2 > span.maxY {
				span.maxY = y2
			}
			if run.BoxWidth > span.width {
				span.width = run.BoxWidth
			}
		}
	}
	for _, row := range l.layout.Rows {
		for _, run := range row.Runs {
			addRun(run, row.Y, 0, 0)
		}
	}
	for _, f := range l.layout.Floats {
		for _, row := range f.Rows {
			for _, run := range row.Runs {
				addRun(run, row.Y, f.X, f.Y)
			}
		}
	}
	// Creation order is outer-to-inner for nested boxes, so a child's surface
	// paints over its parent's instead of being hidden by it.
	ids := make([]int, 0, len(spans))
	for id := range spans {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	for _, id := range ids {
		info, ok := l.doc.Boxes[id]
		if !ok || !info.visual {
			continue
		}
		nested := l.boxHasNestedBlocks(id)
		// A form is one surface made of several direct control/label blocks.
		// Counting them keeps the wrapper from being treated as a single block
		// that already painted itself: each control's fragment used to leave
		// white bands between the fields and a label outside the form fill.
		directBlocks := 0
		for _, b := range l.doc.Blocks {
			if b.BoxOwner == id {
				directBlocks++
			}
		}
		// A box with a single direct block and no nested content is already
		// fully painted by that block. A mixed wrapper (intro text plus a
		// child paragraph, a card plus a nested card, or a form with several
		// controls) is not: the direct blocks' surfaces stop at their own
		// rows, so replace them with the descendant union and paint the whole
		// wrapper once.
		if info.direct && !nested && directBlocks <= 1 {
			continue
		}
		if info.direct && (nested || directBlocks > 1) {
			l.removeBoxPaints(id)
		}
		span := spans[id]
		if span == nil || !span.seen {
			continue
		}
		w := span.width
		if w <= 0 {
			w = span.maxX - span.minX
		}
		if span.maxX > span.minX+w {
			w = span.maxX - span.minX
		}
		if w <= 0 || span.maxY <= span.minY {
			continue
		}
		top := int32(info.style.MarginTop * scale)
		bottom := int32(info.style.MarginBot * scale)
		if top < 0 {
			top = 0
		}
		if bottom < 0 {
			bottom = 0
		}
		// The run's X starts after the wrapper's left padding/margin. The
		// painter wants the border-box edge, so back that inset out here.
		left := int32(info.style.Indent * scale)
		x, y := span.minX-left, span.minY-top
		h := span.maxY - span.minY + top + bottom
		// A wrapper's own min-height/height belongs to its box, not to the
		// child runs that happen to define its top and bottom. Without this a
		// 300px hero with only a heading and a paragraph collapsed to the
		// text height, and its background image never covered the hero.
		if minH := int32(math.Round(max(info.style.MinHeight, info.style.Height) * scale)); minH > h {
			h = minH
		}
		// A card at the top of the page can have padding above its first line;
		// the page clip starts at zero, so keep the painted box on-page and
		// retain its full height instead of drawing a negative strip.
		if x < 0 {
			w += x
			x = 0
		}
		if y < 0 {
			h += y
			y = 0
		}
		if w <= 0 || h <= 0 {
			continue
		}
		l.paintWebBox(info, sdl.Rect{X: x, Y: y, W: w, H: h})
	}
}

// applyBoxConstraints narrows a block's measure to the layout boxes that
// enclose it. Wrappers that only declared a width, max-width, height or
// padding used to be invisible to layout: their child paragraphs ran to the
// page edge, so an inset article's image and list did not line up with its copy.
func (l *webLayouter) applyBoxConstraints(block webBlock, padX, contentW int32) (int32, int32) {
	if l == nil || l.doc == nil || len(block.Boxes) == 0 || l.baseSize <= 0 {
		return padX, contentW
	}
	scale := l.baseSize / 16
	for _, id := range block.Boxes {
		info, ok := l.doc.Boxes[id]
		if !ok {
			continue
		}
		// A block whose style is the same element style already had its own
		// padding and width applied by the ordinary block pass. Only an
		// ancestor wrapper contributes here; otherwise a card's direct text
		// would receive its padding twice.
		if block.BoxOwner == id {
			// This is the element that owns the box. Its own width, padding and
			// auto margins were already applied by the ordinary block pass;
			// applying them again here centred a flex column item twice (once
			// by flexCrossOffset and once by this box).
			continue
		}
		st := info.style
		left := int32(math.Round(st.Indent * scale))
		right := int32(math.Round(st.IndentRight * scale))
		if left < 0 {
			left = 0
		}
		if right < 0 {
			right = 0
		}
		target := contentW
		if st.WidthPct > 0 {
			target = min32(target, int32(math.Round(float64(contentW)*st.WidthPct/100)))
		} else if st.Width > 0 {
			target = min32(target, int32(math.Round(st.Width*scale)))
		}
		if st.MaxWidthPct > 0 {
			target = min32(target, int32(math.Round(float64(contentW)*st.MaxWidthPct/100)))
		} else if st.MaxWidth > 0 {
			target = min32(target, int32(math.Round(st.MaxWidth*scale)))
		}
		if st.MinWidthPct > 0 {
			target = max(target, int32(math.Round(float64(contentW)*st.MinWidthPct/100)))
		} else if st.MinWidth > 0 {
			target = max(target, int32(math.Round(st.MinWidth*scale)))
		}
		if target < 32 {
			target = 32
		}
		if target > contentW {
			target = contentW
		}
		inner := target - left - right
		if inner < 32 {
			left, right, inner = 0, 0, target
		}
		x := padX + left
		if free := contentW - target; free > 0 && (st.MarginLeftAuto || st.MarginRightAuto) {
			switch {
			case st.MarginLeftAuto && st.MarginRightAuto:
				x += free / 2
			case st.MarginLeftAuto:
				x += free
			}
		}
		padX, contentW = x, inner
	}
	return padX, contentW
}

// boxHasNestedBlocks reports whether any block other than the box's direct
// block descends from it. That is the mixed wrapper case: the direct block's
// background covers only its own rows, while a nested paragraph or card sits
// outside that surface.
func (l *webLayouter) boxHasNestedBlocks(id int) bool {
	if l == nil || l.doc == nil || id == 0 {
		return false
	}
	for _, b := range l.doc.Blocks {
		if b.BoxOwner == id {
			continue
		}
		for _, box := range b.Boxes {
			if box == id {
				return true
			}
		}
	}
	return false
}

// removeBoxPaints drops the partial surface a direct block painted for a
// wrapper that also has nested content. Layered fills are removed together so
// a translucent background is not applied twice when the union replaces it.
func (l *webLayouter) removeBoxPaints(id int) {
	if l == nil || l.layout == nil || id == 0 {
		return
	}
	if len(l.layout.Rects) > 0 {
		kept := l.layout.Rects[:0]
		for _, r := range l.layout.Rects {
			if r.BoxID != id {
				kept = append(kept, r)
			}
		}
		l.layout.Rects = kept
	}
	if len(l.layout.Backgrounds) > 0 {
		kept := l.layout.Backgrounds[:0]
		for _, bg := range l.layout.Backgrounds {
			if bg.BoxID != id {
				kept = append(kept, bg)
			}
		}
		l.layout.Backgrounds = kept
	}
}

// boxFlowRect returns the used rectangle of a laid-out visual box before the
// absolute layers are positioned. It is the containing block for an absolutely
// positioned child: an overlay inside a position:relative hero must sit 20px
// from the hero's edge, not 20px from the top of the page.
func (l *webLayouter) boxFlowRect(id int) (sdl.Rect, bool) {
	if l == nil || l.layout == nil || l.doc == nil || id == 0 || l.baseSize <= 0 {
		return sdl.Rect{}, false
	}
	info, ok := l.doc.Boxes[id]
	if !ok {
		return sdl.Rect{}, false
	}
	minX, minY, maxX, maxY := int32(1<<30-1), int32(1<<30-1), int32(0), int32(0)
	width := int32(0)
	seen := false
	for _, row := range l.layout.Rows {
		for _, run := range row.Runs {
			if run.Font == nil {
				continue
			}
			found := false
			for _, box := range run.Boxes {
				if box == id {
					found = true
					break
				}
			}
			if !found {
				continue
			}
			w, h, err := run.Font.SizeUTF8(run.Text)
			if err != nil {
				continue
			}
			seen = true
			minX = min32(minX, run.X)
			minY = min32(minY, row.Y)
			maxX = max(maxX, run.X+int32(w))
			maxY = max(maxY, row.Y+int32(h))
			width = max(width, run.BoxWidth)
		}
	}
	if !seen {
		return sdl.Rect{}, false
	}
	scale := l.baseSize / 16
	left := int32(math.Round(info.style.Indent * scale))
	top := int32(math.Round(info.style.MarginTop * scale))
	bottom := int32(math.Round(info.style.MarginBot * scale))
	if left < 0 {
		left = 0
	}
	if top < 0 {
		top = 0
	}
	if bottom < 0 {
		bottom = 0
	}
	w := width
	if w <= 0 {
		w = maxX - minX
	}
	if maxX > minX+w {
		w = maxX - minX
	}
	h := maxY - minY + top + bottom
	if floor := int32(math.Round(max(info.style.Height, info.style.MinHeight) * scale)); floor > h {
		h = floor
	}
	if w <= 0 || h <= 0 {
		return sdl.Rect{}, false
	}
	return sdl.Rect{X: minX - left, Y: minY - top, W: w, H: h}, true
}

// paintWebBox reuses the ordinary block painter for a box that had no block of
// its own. A throwaway layout is cheaper and much less error-prone than a
// second implementation of gradients, background layers, radii and borders.
func (l *webLayouter) paintWebBox(info webBoxInfo, rect sdl.Rect) {
	if rect.W <= 0 || rect.H <= 0 || l.baseSize <= 0 || l.rowH <= 0 || l.layout == nil {
		return
	}
	style := info.style
	style.Width, style.WidthPct = 0, 0
	style.Height = float64(rect.H) * 16 / l.baseSize
	style.MinHeight, style.MaxHeight = 0, 0
	style.Indent, style.IndentRight = 0, 0
	style.MarginTop, style.MarginBot = 0, 0
	style.PadTop, style.PadBot = 0, 0
	box := webBlock{Style: style}
	box.HasBg, box.Background = style.HasBg, style.Background
	box.BorderLeft, box.BorderRight = style.BorderLeft, style.BorderRight
	box.BorderTop, box.BorderBottom = style.BorderTop, style.BorderBottom
	box.BoxBorder = style.BoxBorder
	box.BorderWidth, box.BorderStyle = style.BorderWidth, style.BorderStyle
	box.BorderSideWidth, box.BorderSideStyle = style.BorderSideWidth, style.BorderSideStyle
	box.BorderSideStyleSet, box.BorderSideColor = style.BorderSideStyleSet, style.BorderSideColor
	box.BorderSideHasColor = style.BorderSideHasColor
	box.BorderColor, box.HasBorderColor = style.BorderColor, style.HasBorderColor
	box.Outline, box.OutlineOffset = style.Outline, style.OutlineOffset
	if style.HasGradient && style.Gradient != nil {
		box.Gradient, box.HasGradient = *style.Gradient, true
	}
	boxLayout := &webLayout{RowH: l.rowH}
	sl := &webLayouter{
		doc: l.doc, layout: boxLayout, rowH: l.rowH, baseSize: l.baseSize,
		fontAt: l.fontAt, fontAtStyle: l.fontAtStyle, spaceWidths: l.spaceWidths,
		sanitized: l.sanitized, containerSizes: l.containerSizes,
		containerRows: make(map[int]webContainerRowSpan),
	}
	sl.layoutBlocks([]webBlock{box}, 0, rect.W)
	for _, r := range boxLayout.Rects {
		r.Rect.X += rect.X
		r.Rect.Y += rect.Y
		l.layout.Rects = append(l.layout.Rects, r)
	}
	for _, bg := range boxLayout.Backgrounds {
		bg.Rect.X += rect.X
		bg.Rect.Y += rect.Y
		l.layout.Backgrounds = append(l.layout.Backgrounds, bg)
	}
}

// gridTrackWidths resolves an explicit grid-template-columns list into the
// actual pixel width of each track. A fixed track keeps its width; the fr
// tracks split what is left in proportion to their factors. Returning nil
// keeps the historical equal-column path for a bare `display:grid`.
func (l *webLayouter) gridTrackWidths(first webBlock, cols int, contentW, gap int32) []int32 {
	if cols <= 0 || first.GridCols <= 0 || cols > len(first.GridWeights) {
		return nil
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	trackSpace := contentW - gap*int32(cols-1)
	if trackSpace <= 0 {
		return nil
	}
	widths := make([]int32, cols)
	fixed := int32(0)
	totalWeight := 0.0
	hasExplicit := false
	for i := 0; i < cols; i++ {
		if first.GridBases[i] > 0 {
			widths[i] = int32(math.Round(first.GridBases[i] * scale))
			if widths[i] < 1 {
				widths[i] = 1
			}
			fixed += widths[i]
			hasExplicit = true
		}
		if first.GridWeights[i] > 0 {
			totalWeight += first.GridWeights[i]
			hasExplicit = true
		}
	}
	if !hasExplicit {
		return nil
	}
	if fixed > trackSpace {
		// The author's fixed tracks cannot fit the page measure. Keep their
		// proportional shape rather than overflowing the content column.
		for i := range widths {
			widths[i] = int32(float64(widths[i]) * float64(trackSpace) / float64(fixed))
			if widths[i] < 1 {
				widths[i] = 1
			}
		}
		fixed = 0
		for _, w := range widths {
			fixed += w
		}
	}
	left := trackSpace - fixed
	if left < 0 {
		left = 0
	}
	remainingWeight := totalWeight
	for i := 0; i < cols; i++ {
		weight := first.GridWeights[i]
		if weight <= 0 {
			continue
		}
		share := int32(0)
		if remainingWeight > 0 {
			share = int32(math.Round(float64(left) * weight / remainingWeight))
		}
		widths[i] += share
		left -= share
		remainingWeight -= weight
	}
	// A grid whose tracks are all fixed but do not fill the row leaves the
	// remaining room as the row's free space (justify-content handles it).
	return widths
}

// layoutFlexRow lays one container's items out side by side: widths from
// width/flex-basis, the rest shared by flex-grow, then justify-content places
// the resulting columns.
func (l *webLayouter) layoutFlexRow(items []webBlock, padX, contentW int32) {
	if len(items) == 0 {
		return
	}
	grouped := webGroupFlexItems(items)
	if len(grouped) == 0 {
		return
	}
	first := grouped[0].blocks[0]
	n := int32(len(grouped))
	gridMode := first.GridCols > 0 || first.GridMin > 0
	autoCount := flexAutoCount(grouped)
	if n == 1 && autoCount == 0 {
		l.layoutBlocksFlat(grouped[0].blocks, padX, contentW)
		return
	}

	gap := int32(first.FlexGapX * l.baseSize / 16)
	if gap < 0 {
		gap = 0
	}
	if limit := contentW / 8; gap > limit {
		gap = limit
	}
	// Wrapping containers on real pages are tag clouds and button rows: when
	// the columns would be slivers, stacked text beats nine 40px columns.
	minCol := int32(72)
	if first.FlexWrap {
		minCol = 110
	}
	tableRow := first.Style.TableRole == webTableRoleCell
	if tableRow {
		// A table cell is not a card: a 40px column is a legitimate width.
		minCol = 1
	}
	var rowEnds []int
	gridCols := first.GridCols
	if gridCols <= 0 && first.GridMin > 0 {
		// `repeat(auto-fit, minmax(220px, 1fr))` has no fixed count: derive
		// it from the measure. This is the card-grid recipe on most modern
		// sites, and treating it as an unconstrained row produced slivers.
		track := int32(first.GridMin * l.baseSize / 16)
		if track < 1 {
			track = 1
		}
		if track > contentW {
			track = contentW
		}
		gridCols = int((contentW + gap) / (track + gap))
		if gridCols < 1 {
			gridCols = 1
		}
		if gridCols > len(grouped) {
			gridCols = len(grouped)
		}
	}
	if cols := gridCols; cols > 0 {
		// Grid tracks may be explicit, but a handheld still needs readable
		// columns. Reduce the count when the stated grid cannot fit here.
		for cols > 1 && int32(cols)*minCol+int32(cols-1)*gap > contentW {
			cols--
		}
		for end := min(cols, len(grouped)); ; end = min(end+cols, len(grouped)) {
			rowEnds = append(rowEnds, end)
			if end == len(grouped) {
				break
			}
		}
	} else if first.FlexWrap {
		// Flex wrapping uses the items' own basis. Counting only a fixed
		// minimum number of columns shrank 200px cards into 100px slivers
		// just to keep every card on one line.
		rowWidth := int32(0)
		for i, item := range grouped {
			basis := l.itemWidth(item.blocks[0], contentW)
			if basis < minCol {
				basis = minCol
			}
			if basis > contentW {
				basis = contentW
			}
			if rowWidth > 0 && rowWidth+gap+basis > contentW {
				rowEnds = append(rowEnds, i)
				rowWidth = 0
			}
			if rowWidth > 0 {
				rowWidth += gap
			}
			rowWidth += basis
		}
		rowEnds = append(rowEnds, len(grouped))
	}
	if len(rowEnds) > 1 {
		start := 0
		for _, end := range rowEnds {
			if start > 0 {
				gapPx := first.FlexGapY * l.baseSize / 16
				gapRows := int(math.Ceil(gapPx / float64(l.rowH)))
				if gapRows > 3 {
					gapRows = 3
				}
				for j := 0; j < gapRows && len(l.layout.Rows) < webMaxRows; j++ {
					l.layout.Rows = append(l.layout.Rows, webRow{Y: int32(len(l.layout.Rows)) * l.rowH})
				}
			}
			l.layoutFlexRow(webFlattenFlexItems(grouped[start:end]), padX, contentW)
			start = end
		}
		return
	}
	avail := contentW - gap*(n-1)
	if !tableRow && avail < n*minCol {
		l.layoutBlocksFlat(webFlattenFlexItems(grouped), padX, contentW)
		return
	}

	gridWidths := l.gridTrackWidths(first, int(n), contentW, gap)
	widths := make([]int32, n)
	containerIDs := make([]int, n)
	fixed := int32(0)
	auto := make([]int, 0, n)
	totalGrow := 0.0
	for _, item := range grouped {
		if grow := item.blocks[0].FlexGrow; grow > 0 {
			totalGrow += grow
		}
	}
	for i, item := range grouped {
		b := item.blocks[0]
		containerIDs[i] = l.itemContainerID(item.blocks)
		if len(gridWidths) > 0 {
			track := gridWidths[i]
			itemW := l.itemWidth(b, track)
			if itemW <= 0 || itemW > track {
				itemW = track
			}
			widths[i] = itemW
			fixed += widths[i]
			continue
		}
		w := l.itemWidth(b, avail)
		if w <= 0 && containerIDs[i] != 0 && b.FlexGrow <= 0 {
			// A content-sized containment container is shrink-to-fit: its
			// own text decides the column, and cqw/cqi inside it then use that
			// measured width instead of the parser's viewport guess.
			w = l.intrinsicContainerWidth(item.blocks)
		}
		if w <= 0 && totalGrow == 0 {
			if a, z := flexAutoMain(b); a || z {
				// An auto-margin item that also has an auto main size is
				// shrink-to-fit, not a share of the row: the free space belongs
				// to its margins.
				w = l.intrinsicContainerWidth(item.blocks)
				if w < minCol {
					w = minCol
				}
			}
		}
		if w <= 0 && totalGrow > 0 {
			// An auto-sized label keeps a readable width while the item with
			// flex-grow receives the rest. A growing zero-basis item needs a
			// small floor so it does not disappear on a tight row.
			w = 24
			if b.FlexGrow <= 0 {
				w = max(minCol, 110)
			}
		}
		widths[i] = w
		if w > 0 {
			fixed += w
			continue
		}
		auto = append(auto, i)
	}

	if fixed > avail {
		// Subtract the overflow in proportion to each item's shrink factor and
		// basis, which is the visible participation flex-shrink describes. If
		// every item refuses to shrink (shrink: 0), the later overflow check
		// stacks the row rather than squeezing an item the author protected.
		excess := fixed - avail
		totalWeight := 0.0
		for i, item := range grouped {
			if widths[i] <= 0 {
				continue
			}
			shrink := item.blocks[0].FlexShrink
			if shrink < 0 {
				shrink = 0
			}
			totalWeight += float64(widths[i]) * shrink
		}
		if totalWeight > 0 {
			fixed = 0
			for i, item := range grouped {
				if widths[i] <= 0 {
					continue
				}
				shrink := item.blocks[0].FlexShrink
				if shrink < 0 {
					shrink = 0
				}
				cut := int32(math.Round(float64(excess) * float64(widths[i]) * shrink / totalWeight))
				widths[i] -= cut
				if widths[i] < 24 {
					widths[i] = 24
				}
				fixed += widths[i]
			}
		} else {
			fixed = 0
			for i := range widths {
				if widths[i] > 0 {
					fixed += widths[i]
				}
			}
		}
	}

	if left := avail - fixed; left > 0 {
		if totalGrow > 0 {
			// Every grow factor participates, including an item whose explicit
			// flex-basis or width already gave it a starting size.
			remaining := left
			remainingGrow := totalGrow
			for i, item := range grouped {
				grow := item.blocks[0].FlexGrow
				if grow <= 0 {
					continue
				}
				add := remaining
				if remainingGrow > grow {
					add = int32(float64(remaining) * grow / remainingGrow)
				}
				widths[i] += add
				remaining -= add
				remainingGrow -= grow
			}
		} else if len(auto) > 0 {
			remaining := left
			for i, idx := range auto {
				share := remaining / int32(len(auto)-i)
				widths[idx] = share
				remaining -= share
			}
		}
	}
	if !tableRow {
		for i := range widths {
			if widths[i] < 24 {
				widths[i] = 24
			}
		}
	}
	totalW := gap * (n - 1)
	for _, w := range widths {
		totalW += w
	}
	if !tableRow && totalW > contentW {
		// The minimum column widths overflow the page: stack instead of
		// painting text past the right edge.
		l.layoutBlocksFlat(webFlattenFlexItems(grouped), padX, contentW)
		return
	}

	// justify-content distributes the room the columns did not use.
	used := gap * (n - 1)
	for _, w := range widths {
		used += w
	}
	free := contentW - used
	if free < 0 {
		free = 0
	}
	start := int32(0)
	gapExtra := int32(0)
	autoStartSpace := make([]int32, n)
	autoEndSpace := make([]int32, n)
	if autoCount > 0 && !gridMode {
		// Auto margins consume the free main-axis space before justify-content.
		autoStartSpace, autoEndSpace = flexAutoSpaces(grouped, free)
	} else if autoCount == 0 && !gridMode {
		switch first.FlexJustify {
		case webJustifyCenter:
			start = free / 2
		case webJustifyEnd:
			start = free
		case webJustifyBetween:
			if n > 1 {
				gapExtra = free / (n - 1)
			}
		case webJustifyAround:
			start = free / (n * 2)
			if n > 0 {
				gapExtra = free / n
			}
		}
	}

	cellX := make([]int32, n)
	if len(gridWidths) > 0 {
		// Each item is placed at the start of its own track. Auto margins
		// resolve against that track, not against the whole row, so a 120px
		// card centres inside a wider 1fr column.
		cursor := padX
		for i, item := range grouped {
			base := cursor
			freeTrack := gridWidths[i] - widths[i]
			a, z := flexAutoMain(item.blocks[0])
			switch {
			case freeTrack > 0 && a && z:
				base += freeTrack / 2
			case freeTrack > 0 && a:
				base += freeTrack
			}
			cellX[i] = base
			cursor += gridWidths[i] + gap
		}
	} else if gridMode && autoCount > 0 {
		// A grid item's auto margins consume the free space inside its track,
		// not the free space of the whole row. Keep the tracks equal and place
		// each item inside its own area.
		trackW := (contentW - gap*int32(n-1)) / n
		if trackW < 1 {
			trackW = 1
		}
		for i, item := range grouped {
			base := padX + int32(i)*(trackW+gap)
			freeTrack := trackW - widths[i]
			a, z := flexAutoMain(item.blocks[0])
			switch {
			case freeTrack > 0 && a && z:
				base += freeTrack / 2
			case freeTrack > 0 && a:
				base += freeTrack
			}
			cellX[i] = base
		}
	} else if first.FlexReverse {
		cursor := padX + contentW - start
		for i := range grouped {
			cursor -= autoStartSpace[i]
			cursor -= widths[i]
			cellX[i] = cursor
			cursor -= autoEndSpace[i]
			if i < len(grouped)-1 {
				cursor -= gap + gapExtra
			}
		}
	} else {
		cursor := padX + start
		for i := range grouped {
			cursor += autoStartSpace[i]
			cellX[i] = cursor
			cursor += widths[i] + autoEndSpace[i] + gap + gapExtra
		}
	}

	cells := make([]flexCell, 0, n)
	maxRows := int32(0)
	for i, item := range grouped {
		b := item.blocks[0]
		sub := &webLayout{RowH: l.rowH}
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
			spaceWidths: l.spaceWidths, sanitized: l.sanitized,
			containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan)}
		subLayouter.primeItemContainerWidths(item.blocks, widths[i])
		subLayouter.layoutBlocksFlat(item.blocks, 0, widths[i])
		l.absorbContainerRows(subLayouter.containerRows)
		if id := containerIDs[i]; id != 0 && l.baseSize > 0 {
			if size := l.containerSizes[id]; size.Height <= 0 {
				size.Height = float64(len(sub.Rows)) * float64(l.rowH) * 16 / l.baseSize
				l.containerSizes[id] = size
			}
		}
		align := b.FlexAlign
		if b.AlignSelf >= 0 {
			align = b.AlignSelf
		}
		cells = append(cells, flexCell{sub: sub, w: widths[i], align: align})
		if rows := int32(len(sub.Rows)); rows > maxRows {
			maxRows = rows
		}
	}

	baseRow := int32(len(l.layout.Rows))
	need := baseRow + maxRows
	for int32(len(l.layout.Rows)) < need && len(l.layout.Rows) < webMaxRows {
		idx := int32(len(l.layout.Rows))
		l.layout.Rows = append(l.layout.Rows, webRow{Y: idx * l.rowH})
	}

	for i, cell := range cells {
		rows := int32(len(cell.sub.Rows))
		drop := int32(0)
		crossStart, crossEnd := flexAutoCross(grouped[i].blocks[0])
		freeRows := maxRows - rows
		switch {
		case crossStart && crossEnd:
			drop = freeRows / 2
		case crossStart:
			drop = freeRows
		case crossEnd:
			drop = 0
		default:
			switch cell.align {
			case webItemCenter:
				drop = freeRows / 2
			case webItemEnd:
				drop = freeRows
			}
		}
		// Merge every layer the item measured, not just its text, boxes and
		// images: a card's gradient/background image, an embedded video and a
		// positioned badge all live beside the rows and used to disappear when
		// the card was a flex or grid item.
		l.mergeFlexSubLayout(cell.sub, cellX[i], baseRow+drop)
	}
}

// layoutFloats measures the absolutely positioned blocks and parks them in the
// layer list. They take no room in the flow; the painter draws them over the
// page in ascending z-index order.
func (l *webLayouter) layoutFloats(blocks []webBlock, padX, contentW int32) {
	if len(blocks) == 0 {
		return
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	pageH := int32(len(l.layout.Rows)) * l.rowH
	for _, b := range blocks {
		w := contentW
		switch {
		case b.WidthPct > 0:
			w = int32(float64(contentW) * b.WidthPct / 100)
		case b.Width > 0:
			w = int32(b.Width * scale)
		case b.BasisPct > 0:
			w = int32(float64(contentW) * b.BasisPct / 100)
		case b.FlexBasis > 0:
			w = int32(b.FlexBasis * scale)
		default:
			// An auto-width layer has no intrinsic width in a text reader.
			// A caption-sized column reads better than a full-bleed strip.
			w = int32(l.baseSize * 18)
			if w < 140 {
				w = 140
			}
			if w > contentW {
				w = contentW
			}
		}
		if w < 60 {
			w = 60
		}
		if w > contentW {
			w = contentW
		}

		sub := &webLayout{RowH: l.rowH}
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
			spaceWidths: l.spaceWidths, sanitized: l.sanitized,
			containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan)}
		subLayouter.layoutBlocksFlat([]webBlock{b}, 0, w)
		l.absorbContainerRows(subLayouter.containerRows)
		if len(sub.Rows) == 0 && len(sub.Rects) == 0 && len(sub.Images) == 0 {
			continue
		}
		h := int32(len(sub.Rows)) * l.rowH

		// Find the nearest positioned ancestor. `top:20px` is measured from
		// that box, not from the document, or an overlay lands under the
		// sticky site header. No positioned ancestor means the page is the
		// containing block, which is what an absolutely positioned banner
		// directly under <body> expects.
		anchor := sdl.Rect{X: padX, Y: 0, W: contentW, H: pageH}
		if b.Style.Position == webPositionFixed {
			// fixed: the viewport is the containing block, so right:0 and
			// bottom:0 reach the screen edge rather than the body's inset.
			anchor = sdl.Rect{X: 0, Y: 0, W: contentW + 2*padX, H: pageH}
		} else {
			for i := len(b.Boxes) - 1; i >= 0; i-- {
				info, ok := l.doc.Boxes[b.Boxes[i]]
				if !ok || info.style.Position != webPositionRelative {
					continue
				}
				if rect, ok := l.boxFlowRect(b.Boxes[i]); ok {
					anchor = rect
				}
				break
			}
		}
		x := anchor.X
		switch {
		case b.HasLeft:
			x = anchor.X + int32(b.PosLeft*scale)
		case b.HasRight:
			x = anchor.X + anchor.W - w - int32(b.PosRight*scale)
		}
		y := anchor.Y
		switch {
		case b.HasTop:
			y = anchor.Y + int32(b.PosTop*scale)
		case b.HasBottom:
			y = anchor.Y + anchor.H - h - int32(b.PosBottom*scale)
		}
		if y < 0 {
			y = 0
		}
		l.layout.Floats = append(l.layout.Floats, webFloat{
			Z:           b.ZIndex,
			X:           x,
			Y:           y,
			Rows:        sub.Rows,
			Rects:       sub.Rects,
			Backgrounds: sub.Backgrounds,
			Images:      sub.Images,
		})
	}
	sort.SliceStable(l.layout.Floats, func(i, j int) bool {
		return l.layout.Floats[i].Z < l.layout.Floats[j].Z
	})
}

// drawWebFloats paints the stacked layers over the flow. It runs inside the
// page's clip rect, so a layer that scrolls out of view is clipped as a whole
// instead of leaking over the toolbar. under selects the layers with a
// negative z-index, which belong behind the text; the rest go on top.
func drawWebFloats(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, offset int32, under bool) {
	for _, f := range layout.Floats {
		if (f.Z < 0) != under {
			continue
		}
		top := rect.Y + f.Y - offset
		if top+int32(len(f.Rows))*layout.RowH <= rect.Y || top >= rect.Y+rect.H {
			continue
		}
		drawWebLayer(r, config, doc, layout, rect, f, top)
	}
}

// drawWebStickies paints the sticky blocks that have scrolled past their
// normal position. A sticky block keeps its flow rows (so it reserves space)
// and this pass adds the pinned copy at its top offset, which is the other
// half of CSS sticky: the page may scroll underneath it while it stays.
func drawWebStickies(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, offset int32) {
	for _, f := range layout.Stickies {
		if offset <= f.Y {
			// The block is still at or above its flow position; the normal
			// painting already shows it there.
			continue
		}
		top := rect.Y + f.Top - f.Y
		if top >= rect.Y+rect.H {
			continue
		}
		drawWebLayer(r, config, doc, layout, rect, f, top)
	}
}

// drawWebLayer paints one positioned block: backgrounds and images below its
// borders and text, matching the order of an in-flow box. Sticky overlays and
// absolutely positioned floats share this body; they differ only in how top
// is derived from the scroll offset.
func drawWebLayer(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, f webFloat, top int32) {
	// Background layers sit under the box borders and content, exactly as
	// they do on an in-flow card. A fixed or sticky header usually carries
	// its only colour here, so dropping these left it transparent.
	for _, bg := range f.Backgrounds {
		y := top + bg.Rect.Y
		if y+bg.Rect.H <= rect.Y || y >= rect.Y+rect.H || bg.Rect.W <= 0 || bg.Rect.H <= 0 {
			continue
		}
		if bg.Image != "" {
			drawWebImage(r, config, doc, webImagePos{
				Key: bg.Image, Rect: bg.Rect, ObjectFit: bg.ObjectFit,
				ObjectPosition: bg.ObjectPosition, Opacity: bg.Opacity,
			}, rect.X+f.X+bg.Rect.X, y)
			continue
		}
		if bg.HasGradient {
			drawWebGradient(r, sdl.Rect{X: rect.X + f.X + bg.Rect.X, Y: y, W: bg.Rect.W, H: bg.Rect.H}, bg.Radius, bg.Gradient)
			continue
		}
		fillRoundedRect(r, rect.X+f.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, bg.Radius, bg.Color)
	}
	for _, im := range f.Images {
		if im.Invisible {
			continue
		}
		y := top + im.Rect.Y
		if im.Rect.H <= 0 || y >= rect.Y+rect.H || y+im.Rect.H <= rect.Y {
			continue
		}
		drawWebImage(r, config, doc, im, rect.X+f.X+im.Rect.X, y)
	}
	for _, bg := range f.Rects {
		if bg.BackgroundFill {
			// The block colour is already in f.Backgrounds, where layers can
			// stack in CSS order; painting it here again would double a
			// translucent fill.
			continue
		}
		y := top + bg.Rect.Y
		if y+bg.Rect.H <= rect.Y || y >= rect.Y+rect.H {
			continue
		}
		if bg.Stroke {
			thickness := bg.Thickness
			if thickness < 1 {
				thickness = 1
			}
			strokeRoundedRect(r, rect.X+f.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 3), thickness, bg.Color)
			continue
		}
		if bg.HasGradient {
			drawWebGradient(r, sdl.Rect{X: rect.X + f.X + bg.Rect.X, Y: y, W: bg.Rect.W, H: bg.Rect.H}, webRectCorner(bg, 2), bg.Gradient)
			continue
		}
		fillRoundedRect(r, rect.X+f.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 2), bg.Color)
	}
	for _, row := range f.Rows {
		y := top + row.Y
		if y >= rect.Y+rect.H {
			break
		}
		for _, run := range row.Runs {
			if run.Invisible {
				continue
			}
			if run.Font == nil {
				continue
			}
			if run.HasBg {
				if w, h, err := run.Font.SizeUTF8(run.Text); err == nil {
					fillRoundedRect(r, rect.X+f.X+run.X-2, y-1, int32(w)+4, int32(h), 3, run.Bg)
				}
			}
			drawTextRun(r, config, run.Font, run.Text, run.Color, rect.X+f.X+run.X, y)
			if run.Bold {
				drawTextRun(r, config, run.Font, run.Text, run.Color, rect.X+f.X+run.X+1, y)
			}
		}
	}
}
