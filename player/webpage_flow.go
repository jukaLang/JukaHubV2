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
	if len(b.Runs) > 0 || b.Rule || b.Image || b.Video {
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
	if style.BoxSizing == 1 && !autoWidth {
		// webBlockContentWidth already removed the padding from an explicit
		// border-box width; nothing further to do here.
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
	endRow = startRow + int32(len(sub.Rows))
	l.layout.Floats = append(l.layout.Floats, webFloat{
		Z:      block.ZIndex,
		X:      x,
		Y:      startRow * l.rowH,
		Rows:   sub.Rows,
		Rects:  sub.Rects,
		Images: sub.Images,
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
	if n == 1 {
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
	if avail < n*minCol {
		l.layoutBlocksFlat(webFlattenFlexItems(grouped), padX, contentW)
		return
	}

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
		w := l.itemWidth(b, avail)
		if w <= 0 && containerIDs[i] != 0 && b.FlexGrow <= 0 {
			// A content-sized containment container is shrink-to-fit: its
			// own text decides the column, and cqw/cqi inside it then use that
			// measured width instead of the parser's viewport guess.
			w = l.intrinsicContainerWidth(item.blocks)
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
	for i := range widths {
		if widths[i] < 24 {
			widths[i] = 24
		}
	}
	totalW := gap * (n - 1)
	for _, w := range widths {
		totalW += w
	}
	if totalW > contentW {
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

	cells := make([]flexCell, 0, n)
	maxRows := int32(0)
	x := padX + start
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
		x += widths[i] + gap + gapExtra
	}

	baseRow := int32(len(l.layout.Rows))
	need := baseRow + maxRows
	for int32(len(l.layout.Rows)) < need && len(l.layout.Rows) < webMaxRows {
		idx := int32(len(l.layout.Rows))
		l.layout.Rows = append(l.layout.Rows, webRow{Y: idx * l.rowH})
	}

	x = padX + start
	for _, cell := range cells {
		rows := int32(len(cell.sub.Rows))
		drop := int32(0)
		switch cell.align {
		case webItemCenter:
			drop = (maxRows - rows) / 2
		case webItemEnd:
			drop = maxRows - rows
		}
		for i, row := range cell.sub.Rows {
			target := baseRow + drop + int32(i)
			if target < 0 || target >= int32(len(l.layout.Rows)) {
				continue
			}
			for _, run := range row.Runs {
				run.X += x
				l.layout.Rows[target].Runs = append(l.layout.Rows[target].Runs, run)
			}
		}
		for _, r := range cell.sub.Rects {
			r.Rect.X += x
			r.Rect.Y += (baseRow + drop) * l.rowH
			l.layout.Rects = append(l.layout.Rects, r)
		}
		for _, im := range cell.sub.Images {
			im.Rect.X += x
			im.Rect.Y += (baseRow + drop) * l.rowH
			l.layout.Images = append(l.layout.Images, im)
		}
		x += cell.w + gap + gapExtra
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

		x := padX
		switch {
		case b.HasLeft:
			x = padX + int32(b.PosLeft*scale)
		case b.HasRight:
			x = padX + contentW - w - int32(b.PosRight*scale)
		}
		y := int32(0)
		switch {
		case b.HasTop:
			y = int32(b.PosTop * scale)
		case b.HasBottom:
			y = pageH - h - int32(b.PosBottom*scale)
		}
		if y < 0 {
			y = 0
		}
		l.layout.Floats = append(l.layout.Floats, webFloat{
			Z:      b.ZIndex,
			X:      x,
			Y:      y,
			Rows:   sub.Rows,
			Rects:  sub.Rects,
			Images: sub.Images,
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
}
