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
	"unicode/utf8"

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

// webFlexContainerHeight returns the block size a flex column can distribute.
// Percentage heights have already been resolved against a definite parent;
// an unresolved percentage stays content-sized. A min-height is a usable
// floor for the container's distribution.
func webFlexContainerHeight(st webStyle) float64 {
	if st.Height > 0 {
		return webDefiniteContentHeight(st)
	}
	return webHeightContentTarget(st, st.MinHeight)
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
	l.mergeFlexSubLayoutShift(sub, x, rowOffset, 0)
}

// mergeFlexSubLayoutShift is mergeFlexSubLayout with the item dropped a
// further shift pixels inside its rows: a centred item in a flex line, a grid
// item at its track's exact pixel edge. The page keeps its uniform row grid —
// each run stays in the page row its own row maps to and is drawn lower by
// the shift and by any offset its row had in the item (a tall line centred in
// its slots, a relatively positioned block), which used to be lost here.
func (l *webLayouter) mergeFlexSubLayoutShift(sub *webLayout, x, rowOffset, shift int32) {
	if sub == nil {
		return
	}
	dy := rowOffset*l.rowH + shift
	// A table inside the item keeps its last row's bottom padding row below
	// what it measured, so the page reaches it (webpage.go, PaintBottom).
	if bottom := sub.PaintBottom + dy; bottom > l.layout.PaintBottom {
		l.layout.PaintBottom = bottom
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
		// Where the row's text belongs on the page, against where the page row
		// it joins is drawn.
		extra := dy + row.Y - l.layout.Rows[target].Y
		for _, run := range row.Runs {
			run.X += x
			run.VerticalOffset += extra
			if run.HasClip {
				run.Clip.X += x
				run.Clip.Y += dy
			}
			l.layout.Rows[target].Runs = append(l.layout.Rows[target].Runs, run)
		}
	}
	for _, rect := range sub.Rects {
		rect.Rect.X += x
		rect.Rect.Y += dy
		if rect.HasClip {
			rect.Clip.X += x
			rect.Clip.Y += dy
		}
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
		if im.HasClip {
			im.Clip.X += x
			im.Clip.Y += dy
		}
		l.layout.Images = append(l.layout.Images, im)
	}
	for _, video := range sub.Videos {
		video.Rect.X += x
		video.Rect.Y += dy
		if video.HasClip {
			video.Clip.X += x
			video.Clip.Y += dy
		}
		l.layout.Videos = append(l.layout.Videos, video)
	}
	for _, float := range sub.Floats {
		float.X += x
		float.Y += dy
		l.layout.Floats = append(l.layout.Floats, float)
	}
	// A positioned subtree that started inside the item keeps its static
	// position, moved with the item (webpage_layers.go).
	l.mergeStatics(sub, x, dy)
	for id, h := range sub.boxStretch {
		l.stretchBox(id, h)
	}
	l.mergeBoxGeom(sub, x)
}

// webBoxGeom is a layout box's border box across, in layout pixels: where
// applyBoxConstraints put it (X) and how wide it made it (W).
type webBoxGeom struct {
	X, W int32
}

// noteBoxGeom records box id's border box the first time layout applies the
// box; every block inside it applies it the same way.
func (l *webLayouter) noteBoxGeom(id int, x, w int32) {
	if l == nil || l.layout == nil || id == 0 || w <= 0 {
		return
	}
	if l.layout.boxGeom == nil {
		l.layout.boxGeom = make(map[int]webBoxGeom, 16)
	}
	if _, ok := l.layout.boxGeom[id]; !ok {
		l.layout.boxGeom[id] = webBoxGeom{X: x, W: w}
	}
}

// mergeBoxGeom takes the box geometry a sub-layout measured at x = 0 (a flex
// or grid item, a float) into this layout, moved dx across. A box this layout
// applied itself keeps its own.
func (l *webLayouter) mergeBoxGeom(sub *webLayout, dx int32) {
	if sub == nil {
		return
	}
	for id, g := range sub.boxGeom {
		l.noteBoxGeom(id, g.X+dx, g.W)
	}
}

// stretchBox records that box id is painted at least h layout pixels tall:
// a flex or grid item stretched to its line or track (layout.boxStretch).
func (l *webLayouter) stretchBox(id int, h int32) {
	if l == nil || l.layout == nil || id == 0 || h <= 0 {
		return
	}
	if l.layout.boxStretch == nil {
		l.layout.boxStretch = make(map[int]int32, 4)
	}
	if h > l.layout.boxStretch[id] {
		l.layout.boxStretch[id] = h
	}
}

// stretchFlexItem gives a stretched flex or grid item the cross size of its
// line or track: the surface its own block painted in the sub-layout
// (stretchGridItemBackgrounds), and the box of a wrapper item, which the
// container pass paints later from its runs (stretchBox). An item with a
// height of its own keeps it.
func (l *webLayouter) stretchFlexItem(sub *webLayout, b webBlock, contentH, lineH int32) {
	if sub == nil || lineH <= contentH || b.Style.Height > 0 || l.doc == nil {
		return
	}
	stretchGridItemBackgrounds(sub, contentH, lineH)
	depth := l.doc.FlexItemBoxDepth[b.FlexGroup][b.FlexItem]
	if depth <= 0 || depth > len(b.Boxes) {
		return
	}
	id := b.Boxes[depth-1]
	if info, ok := l.doc.Boxes[id]; ok && info.style.Height <= 0 && !info.anchor {
		l.stretchBox(id, lineH)
	}
}

// mergeStatics moves a sublayout's layer static positions into this layout.
func (l *webLayouter) mergeStatics(sub *webLayout, dx, dy int32) {
	if len(sub.statics) == 0 {
		return
	}
	if l.layout.statics == nil {
		l.layout.statics = make(map[int]webStaticPos, len(sub.statics))
	}
	for id, pos := range sub.statics {
		l.layout.statics[id] = webStaticPos{X: pos.X + dx, Y: pos.Y + dy}
	}
}

// mergeFlexSubLayoutAtY merges a measured sublayout at an exact pixel Y.
// Explicit grid tracks need their authored pixel edges (a 40px track is not
// two 29px rows), so the paint layers keep the exact offset and each text row
// joins the page row it falls in, drawn the rest of the way down
// (mergeFlexSubLayoutShift). It used to append a page row at every new Y: rows
// out of grid order, painted out of turn, and — the page's height being its
// row count — a blank stretch after every grid whose tracks were not whole
// rows.
func (l *webLayouter) mergeFlexSubLayoutAtY(sub *webLayout, x, y int32) {
	if sub == nil || l.rowH <= 0 {
		return
	}
	rowOffset := y / l.rowH
	if y < 0 && y%l.rowH != 0 {
		rowOffset--
	}
	l.mergeFlexSubLayoutShift(sub, x, rowOffset, y-rowOffset*l.rowH)
}

// layoutFlexSingle lays out a flex row's only item where its container puts
// it. justify-content center or end moves an item narrower than the row (a
// lone button, a logo) along it — the item is as wide as its content, as
// flex-basis: auto makes it — and a definite container height (a 64px header
// around one wrapper, a full-screen hero around one block) gives
// align-items/align-self center or end, or a top auto margin, room to drop it.
// It reports false, having done nothing, when the item simply fills the row,
// as every single item used to.
func (l *webLayouter) layoutFlexSingle(item webFlexItem, first webBlock, padX, contentW int32) bool {
	if len(item.blocks) == 0 || l.baseSize <= 0 || l.rowH <= 0 {
		return false
	}
	b := item.blocks[0]
	x, w := padX, contentW
	justify := first.FlexJustify
	if first.FlexReverse {
		// row-reverse starts at the right.
		switch justify {
		case webJustifyStart:
			justify = webJustifyEnd
		case webJustifyEnd:
			justify = webJustifyStart
		}
	}
	if justify != webJustifyStart && justify != webJustifyBetween && b.FlexGrow <= 0 {
		// The item is laid out at its own width, as each item of a longer
		// row is (layoutFlexRowItems): its width, basis or max-width, or its
		// content's.
		itemW := l.itemWidth(b, contentW)
		if itemW <= 0 {
			itemW = l.shrinkToFitWidth(item.blocks, contentW)
		}
		if itemW > 0 && itemW < contentW {
			shift := (contentW - itemW) / 2
			if justify == webJustifyEnd {
				shift = contentW - itemW
			}
			x, w = padX+shift, itemW
		}
	}
	align := b.FlexAlign
	if b.AlignSelf >= 0 {
		align = b.AlignSelf
	}
	crossStart, _ := flexAutoCross(b)
	crossMoves := first.FlexHeight > 0 && (align == webItemCenter || align == webItemEnd || crossStart)
	if x == padX && w == contentW && !crossMoves {
		return false
	}
	sub := &webLayout{RowH: l.rowH}
	sl := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, constraintSkip: l.constraintSkip, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
		spaceWidths: l.spaceWidths, sanitized: l.sanitized, boxOverride: l.boxOverride,
		containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan)}
	sl.primeItemContainerWidths(item.blocks, w)
	sl.layoutBlocksFlat(item.blocks, 0, w)
	l.absorbContainerRows(sl.containerRows)
	extent := webFlexCellExtent(sub)
	lineH, drop := extent, int32(0)
	if crossMoves {
		lineH = max(extent, int32(math.Round(first.FlexHeight*l.baseSize/16)))
		drop = webFlexCrossDrop(b, align, lineH-extent)
	}
	baseRow := int32(len(l.layout.Rows))
	need := baseRow + max(int32(len(sub.Rows)), (lineH+l.rowH-1)/l.rowH)
	for int32(len(l.layout.Rows)) < need && len(l.layout.Rows) < webMaxRows {
		l.layout.Rows = append(l.layout.Rows, webRow{Y: int32(len(l.layout.Rows)) * l.rowH})
	}
	l.mergeFlexSubLayoutShift(sub, x, baseRow+drop/l.rowH, drop%l.rowH)
	return true
}

// layoutFlexColumnAuto lays out a column flex container with a definite
// block height. It measures each item, hands the spare rows to the items'
// auto margins (the same flexAutoSpaces allocator the row uses) or, when no
// item has one, to justify-content — a hero's heading and button in the
// middle or at the foot of a full-screen section — and merges the items at
// those offsets, each placed across the column by flexColumnCross.
func (l *webLayouter) layoutFlexColumnAuto(grouped []webFlexItem, block webBlock, padX, contentW, containerRows int32) {
	type measuredItem struct {
		item     webFlexItem
		sub      *webLayout
		x        int32
		rows     int32
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
		x, w := l.flexColumnCross(blocks, padX, contentW)
		sub := &webLayout{RowH: l.rowH}
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, constraintSkip: l.constraintSkip, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
			spaceWidths: l.spaceWidths, sanitized: l.sanitized,
			containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan)}
		subLayouter.primeItemContainerWidths(blocks, w)
		subLayouter.layoutBlocksFlat(blocks, 0, w)
		l.absorbContainerRows(subLayouter.containerRows)
		rows := int32(len(sub.Rows))
		if rows < 1 && webItemVisible(blocks[0]) {
			rows = 1
		}
		total += gapAbove + rows
		measured = append(measured, measuredItem{item: item, sub: sub, x: x, rows: rows, gapAbove: gapAbove})
	}
	free := containerRows - total
	if free < 0 {
		free = 0
	}
	startSpace, endSpace := flexAutoSpaces(grouped, free)
	lead, between := int32(0), int32(0)
	if flexAutoCount(grouped) == 0 && free > 0 && len(measured) > 0 {
		n := int32(len(measured))
		justify := block.FlexJustify
		if block.FlexReverse {
			// column-reverse starts at the bottom: its start packs the items
			// at the foot and its end at the top (the caller has already put
			// the items in visual order).
			switch justify {
			case webJustifyStart:
				justify = webJustifyEnd
			case webJustifyEnd:
				justify = webJustifyStart
			}
		}
		switch justify {
		case webJustifyCenter:
			lead = free / 2
		case webJustifyEnd:
			lead = free
		case webJustifyBetween:
			if n > 1 {
				between = free / (n - 1)
			}
		case webJustifyAround:
			between = free / n
			lead = between / 2
		case webJustifyEvenly:
			between = free / (n + 1)
			lead = between
		}
	}
	baseRow := int32(len(l.layout.Rows))
	cursor := baseRow + lead
	for i := range measured {
		m := &measured[i]
		cursor += m.gapAbove
		if i > 0 {
			cursor += between
		}
		before, after := startSpace[i], endSpace[i]
		if block.FlexReverse {
			// flexAutoMain reports main-start/main-end. In a column-reverse
			// container the visual top is the main-end edge, so the gaps swap.
			before, after = after, before
		}
		cursor += before
		l.mergeFlexSubLayout(m.sub, m.x, cursor)
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

// flexColumnCross places a column flex item across the column: x is its left
// edge and w the measure it is laid out in. Auto margins come first
// (flexCrossOffset). Otherwise align-self, or the container's align-items,
// decides: stretch (the default) keeps the whole column; center and end move
// an item without a width of its own to the middle or the right at its
// content's width, as a browser shrinks it — a hero's heading and button sit
// in the middle with no text-align — and start shrinks an item that paints a
// box (a tag, a badge) so its surface hugs its text.
func (l *webLayouter) flexColumnCross(blocks []webBlock, padX, contentW int32) (int32, int32) {
	if len(blocks) == 0 || l.baseSize <= 0 {
		return padX, contentW
	}
	if shift := l.flexCrossOffset(blocks, contentW); shift > 0 {
		return padX + shift, contentW
	}
	b := blocks[0]
	if start, end := flexAutoCross(b); start || end {
		return padX, contentW
	}
	align := b.FlexAlign
	if b.AlignSelf >= 0 {
		align = b.AlignSelf
	}
	if align == webItemStretch {
		return padX, contentW
	}
	scale := l.baseSize / 16
	if b.Width > 0 || b.WidthPct > 0 {
		// The item has a width of its own. When its first block is the item
		// itself, the block pass applies that width and only the place moves;
		// a wrapper's width lives on its box, which the item's layout does
		// not apply again, so it keeps the column as before.
		if b.Style.Width != b.Width || b.Style.WidthPct != b.WidthPct {
			return padX, contentW
		}
		w := webBlockContentWidth(b, contentW, l.baseSize) + int32(math.Round((b.Indent+b.Style.IndentRight)*scale))
		if free := contentW - w; free > 0 {
			switch align {
			case webItemCenter:
				return padX + free/2, contentW
			case webItemEnd:
				return padX + free, contentW
			}
		}
		return padX, contentW
	}
	if align == webItemStart && !b.HasBg && !b.HasGradient && !b.BoxBorder && !b.Style.HasBoxShadow {
		return padX, contentW
	}
	w := l.shrinkToFitWidth(blocks, contentW)
	if w <= 0 || w >= contentW {
		return padX, contentW
	}
	switch align {
	case webItemCenter:
		return padX + (contentW-w)/2, w
	case webItemEnd:
		return padX + contentW - w, w
	}
	return padX, w
}

// shrinkToFitWidth is the width a flex item without one of its own takes when
// it is not stretched: its widest line — one block's runs side by side, the
// blocks under each other — with its padding, never wider than avail. 0 when
// the item cannot be measured that way (a nested flex or grid row, a table, a
// video or a preformatted block): it then keeps the whole column.
func (l *webLayouter) shrinkToFitWidth(blocks []webBlock, avail int32) int32 {
	if l.baseSize <= 0 || avail <= 0 {
		return 0
	}
	scale := l.baseSize / 16
	widest := int32(0)
	for bi := range blocks {
		b := &blocks[bi]
		if len(b.FlexTrail) > 0 || b.Style.TableRole != webTableRoleNone || b.Video || b.Placeholder || b.Rule ||
			!webWhiteSpaceWraps(b.WhiteSpace) || b.Pre || b.Style.WidthPct > 0 {
			return 0
		}
		pad := int32(math.Round((b.Indent + b.Style.IndentRight) * scale))
		if b.Image {
			iw := l.intrinsicImageWidth(b)
			if iw <= 0 {
				// A picture of unknown or relative width cannot say how wide
				// its item is.
				return 0
			}
			widest = max(widest, iw+pad)
			continue
		}
		if b.Style.Width > 0 {
			// A block with a width of its own is that wide, whatever its text.
			bw := int32(math.Round(b.Style.Width * scale))
			if b.Style.BoxSizing != 1 {
				bw += pad
			}
			widest = max(widest, bw)
			continue
		}
		line, lineMax := int32(0), int32(0)
		for ri, run := range b.Runs {
			if run.Image != nil {
				iw := l.intrinsicImageWidth(run.Image)
				if iw <= 0 {
					return 0
				}
				line += iw
				continue
			}
			if ctrl := run.Style.Control; ctrl != nil && (ctrl.Kind == "text" || ctrl.Kind == "textarea") {
				w := int32(ctrl.Width * scale)
				if ctrl.WidthPct > 0 {
					w = int32(float64(avail) * ctrl.WidthPct / 100)
				}
				if w <= 0 {
					w = int32(l.baseSize * 12)
				}
				line += w
				continue
			}
			text := run.Text
			if ri == 0 {
				text = strings.TrimLeft(text, " \t")
			}
			if ri == len(b.Runs)-1 {
				text = strings.TrimRight(text, " \t")
			}
			font := l.styledFont(run.Style)
			if font == nil {
				return 0
			}
			// The margin, border and padding of the inline boxes the run opens
			// and closes (webpage_inlinebox.go).
			line += webInlineInsets(&run, l.baseSize)
			for si, segment := range strings.Split(text, "\n") {
				if si > 0 {
					// A line break: the next segment starts a new line.
					lineMax = max(lineMax, line)
					line = 0
				}
				segment = webTransformWord(segment, run.Style.Transform)
				if segment == "" {
					continue
				}
				w, _, err := font.SizeUTF8(segment)
				if err != nil {
					return 0
				}
				line += int32(w) + int32(math.Round(run.Style.LetterSpacing*scale))*int32(utf8.RuneCountInString(segment))
			}
		}
		widest = max(widest, max(lineMax, line)+pad)
	}
	if widest <= 0 {
		return 0
	}
	// The line builder measures word by word; a few pixels of slack keep the
	// rounding from wrapping the last word.
	return min(widest+int32(math.Ceil(6*scale)), avail)
}

// fitBlockWidth is how wide one block is under width: fit-content or
// max-content (mode 1: its longest line set without wrapping) or min-content
// (mode 2: its widest word), its padding included, in layout pixels. 0 when
// its content cannot say (a table, preformatted text, a picture of unknown
// size) or it has none.
func (l *webLayouter) fitBlockWidth(b *webBlock, mode int) int32 {
	if mode == 2 {
		return l.intrinsicMinContentWidth([]webBlock{*b})
	}
	return l.shrinkToFitWidth([]webBlock{*b}, math.MaxInt32/2)
}

// fitBoxWidth is the width of the layout box id when it is sized by its
// content (webStyle.WidthFit), in layout pixels: the widest of the blocks
// inside it (fitBlockWidth) with the box's own padding round the nested ones.
// 0 for any other box, and when one of the blocks cannot say or the box holds
// a flex row, a table or a float, whose widths do not simply stack.
func (l *webLayouter) fitBoxWidth(id int) int32 {
	if l == nil || l.doc == nil || id == 0 {
		return 0
	}
	if w, ok := l.fitWidths[id]; ok {
		return w
	}
	info, ok := l.doc.Boxes[id]
	if !ok || info.style.WidthFit == 0 || info.style.Width > 0 || info.style.WidthPct > 0 {
		return 0
	}
	scale := l.baseSize / 16
	pad := int32(math.Round((info.style.Indent + info.style.IndentRight) * scale))
	widest := int32(0)
	for i := range l.doc.Blocks {
		b := &l.doc.Blocks[i]
		at := -1
		for k, box := range b.Boxes {
			if box == id {
				at = k
				break
			}
		}
		if at < 0 {
			continue
		}
		if b.Float || b.Style.FloatSide != 0 || b.Style.TableRole != webTableRoleNone {
			widest = 0
			break
		}
		if b.FlexGroup != 0 {
			// A flex row or a grid inside the box sets its items side by side;
			// one the box itself sits in is no concern of its width.
			if depth, known := l.doc.FlexBoxDepth[b.FlexGroup]; !known || depth > at {
				widest = 0
				break
			}
		}
		w := l.fitBlockWidth(b, info.style.WidthFit)
		if w <= 0 {
			if len(b.Runs) > 0 || b.Image || b.Video || b.Rule || b.Placeholder {
				widest = 0
				break
			}
			continue
		}
		if b.BoxOwner != id {
			w += pad
		}
		widest = max(widest, w)
	}
	if l.fitWidths == nil {
		l.fitWidths = make(map[int]int32, 4)
	}
	l.fitWidths[id] = widest
	return widest
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
	oldSkip := l.constraintSkip
	if len(blocks) > 0 && l.doc != nil {
		group, item := blocks[0].FlexGroup, blocks[0].FlexItem
		depth := l.doc.FlexBoxDepth[group]
		if itemDepth := l.doc.FlexItemBoxDepth[group][item]; itemDepth > depth {
			depth = itemDepth
		}
		if depth > l.constraintSkip {
			l.constraintSkip = depth
		}
	}
	defer func() { l.constraintSkip = oldSkip }()
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

// layoutCSSFloat measures a `float: left/right` block of its own (a floated
// picture, a floated paragraph) at its own width and parks it in the layer
// list at the flow row startRow: at its side of the containing block (padX,
// contentW), beside the floats already there, or below them when it does not
// fit (webpage_floats.go). A picture of known size is as wide as it draws and
// keeps its own margins as the gap to the text. It returns the room it takes,
// which the flow wraps round and `clear` moves below. A float does not advance
// the flow itself, exactly like CSS.
func (l *webLayouter) layoutCSSFloat(block webBlock, padX, contentW, startRow int32, reserves []webFloatReserve) (webFloatReserve, bool) {
	if contentW <= 0 || l.rowH <= 0 {
		return webFloatReserve{}, false
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	style := block.Style
	width, marginL, marginR, marginB := int32(0), int32(0), int32(0), int32(0)
	picture := false
	if block.Image {
		// An image block's style is the picture's own: its margins are the
		// gap a page puts between a floated photo and the text.
		if iw := l.floatPictureWidth(&block, contentW); iw > 0 {
			width, picture = iw, true
			marginL = max(int32(math.Round((style.Indent-style.PadLeft)*scale)), 0)
			marginR = max(int32(math.Round((style.IndentRight-style.PadRight)*scale)), 0)
			marginB = max(int32(math.Round(style.MarginBot*scale)), 0)
		}
	}
	if !picture {
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
	}
	row, left, right := webFloatPlace(reserves, startRow, padX, contentW, width+marginL+marginR)
	maxWidth := right - left - marginL - marginR
	if maxWidth < 16 {
		maxWidth = contentW - marginL - marginR
	}
	if width > maxWidth {
		width = maxWidth
	}
	if width < 16 {
		width = 16
	}
	x := left + marginL
	if style.FloatSide == 2 {
		x = right - marginR - width
		if x < left+marginL {
			x = left + marginL
		}
	}

	inner := block
	inner.MarginTop, inner.MarginBot = 0, 0
	inner.Float = false
	inner.Style.FloatSide = 0
	inner.Style.Clear = 0
	inner.Style.Position = webPositionStatic
	if picture {
		// The picture fills its float box at the width found above (a
		// percentage was of the column, not of the box), and neither its
		// paragraph's alignment nor the boxes around the float move it.
		inner.Align = 0
		inner.Style.Width, inner.Style.WidthPct = float64(width)/scale, 0
		inner.Style.MinWidth, inner.Style.MaxWidth = 0, 0
		inner.Style.MinWidthPct, inner.Style.MaxWidthPct = 0, 0
	} else {
		inner.Style.Width, inner.Style.WidthPct = 0, 0
		inner.Style.MinWidth, inner.Style.MaxWidth = 0, 0
		inner.Style.MinWidthPct, inner.Style.MaxWidthPct = 0, 0
	}
	sub := &webLayout{RowH: l.rowH}
	sl := &webLayouter{
		doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
		spaceWidths:    map[*ttf.Font]int32{},
		sanitized:      make(map[webSanitizeKey]string, 64),
		containerSizes: l.containerSizes,
		containerRows:  make(map[int]webContainerRowSpan),
	}
	if picture {
		sl.constraintSkip = len(block.Boxes)
	}
	sl.layoutBlocks([]webBlock{inner}, 0, width)
	if len(sub.Rows) == 0 {
		return webFloatReserve{}, false
	}
	// The float's flow reserve covers its painted box, not just its text
	// rows: a padded float otherwise let `clear` put the next paragraph over
	// its bottom padding. min-height/height are floors here just as they are
	// in the ordinary block box.
	padTop := int32(math.Round(block.Style.PadTop * scale))
	padBot := int32(math.Round(block.Style.PadBot * scale))
	if padTop < 0 || picture {
		padTop = 0
	}
	if padBot < 0 || picture {
		padBot = 0
	}
	boxH := int32(len(sub.Rows))*l.rowH + padTop + padBot
	if picture {
		boxH = max(webLayerExtent(sub), l.rowH)
	} else if floor := int32(math.Round(max(block.Style.Height, block.Style.MinHeight) * scale)); floor > boxH {
		boxH = floor + padTop + padBot
	}
	endRow := row + int32(math.Ceil(float64(boxH+marginB)/float64(l.rowH)))
	// The innermost box the float sits in places it inside a sticky bar,
	// whose pinned copy carries it (webCollectStickies).
	root := 0
	if n := len(block.Boxes); n > 0 {
		root = block.Boxes[n-1]
	}
	// A float paints over the flow's surfaces and under its text and every
	// positioned layer (webpage_layers.go), in document order.
	l.layout.Floats = append(l.layout.Floats, webFloat{
		Z:           block.ZIndex,
		X:           x,
		Y:           row * l.rowH,
		H:           boxH,
		W:           width,
		Phase:       webLayerFloat,
		Key:         webFreeStackKey(block.ZIndex, block.TreeSeq),
		Root:        root,
		Rows:        sub.Rows,
		Rects:       sub.Rects,
		Backgrounds: sub.Backgrounds,
		Images:      sub.Images,
		Videos:      sub.Videos,
	})
	l.mergeBoxGeom(sub, x)
	return webFloatReserve{
		side: style.FloatSide, startRow: row, endRow: endRow,
		x0: x - marginL, x1: x + width + marginR, seq: block.TreeSeq,
	}, true
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
		// A sized image (a logo, an icon) is as wide as it draws.
		if b.Image {
			if width := l.intrinsicImageWidth(&b) + padding; width > widest {
				widest = width
			}
			continue
		}
		for _, run := range b.Runs {
			if run.Image != nil {
				if width := l.intrinsicImageWidth(run.Image) + padding; width > widest {
					widest = width
				}
				continue
			}
			text := strings.TrimSpace(run.Text)
			if text == "" {
				continue
			}
			font := l.styledFont(run.Style)
			if font == nil {
				continue
			}
			w, _, err := font.SizeUTF8(text)
			if err != nil {
				continue
			}
			if width := int32(w) + webInlineInsets(&run, l.baseSize) + padding; width > widest {
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

// intrinsicMinContentWidth measures the narrowest width at which an item's
// text can still wrap without breaking a word. It is the min-content floor a
// flex item's automatic minimum size asks for. The measurement reuses the same
// font objects and SizeUTF8 path as intrinsicContainerWidth; it only chooses
// words instead of whole runs, so a long label keeps its word intact in a
// shrinking row.
func (l *webLayouter) intrinsicMinContentWidth(blocks []webBlock) int32 {
	if l.fontAt == nil || l.baseSize <= 0 {
		return 0
	}
	scale := l.baseSize / 16
	widest := int32(0)
	for _, b := range blocks {
		padding := int32((b.Indent + b.Style.IndentRight) * scale)
		if b.Image {
			if width := l.intrinsicImageWidth(&b) + padding; width > widest {
				widest = width
			}
			continue
		}
		for _, run := range b.Runs {
			if run.Image != nil {
				if width := l.intrinsicImageWidth(run.Image) + padding; width > widest {
					widest = width
				}
				continue
			}
			text := strings.TrimSpace(run.Text)
			if text == "" {
				continue
			}
			font := l.styledFont(run.Style)
			if font == nil {
				continue
			}
			chunks := strings.Fields(text)
			if !webWhiteSpaceWraps(run.Style.WhiteSpace) || run.Style.Pre {
				chunks = strings.Split(text, "\n")
			}
			// The run's first word comes after the left edges of the inline
			// boxes it opens and its last before the right edges of those it
			// closes (webpage_inlinebox.go).
			lead := int32(math.Round(run.Lead * l.baseSize / 16))
			trail := int32(math.Round(run.Trail * l.baseSize / 16))
			for ci, chunk := range chunks {
				chunk = strings.TrimSpace(chunk)
				if chunk == "" {
					continue
				}
				edges := int32(0)
				if ci == 0 {
					edges += lead
				}
				if ci == len(chunks)-1 {
					edges += trail
				}
				// anywhere and break-all create legal break points between
				// glyphs. Their min-content floor is the widest glyph, not the
				// whole identifier. break-word keeps the unbroken min-content
				// size, as CSS specifies for intrinsic track sizing.
				if webWhiteSpaceWraps(run.Style.WhiteSpace) &&
					(run.Style.WordBreak == 1 || run.Style.OverflowWrap == 1) && len(chunk) <= 8192 {
					for _, r := range chunk {
						w, _, err := font.SizeUTF8(string(r))
						if err == nil {
							if width := int32(w) + padding; width > widest {
								widest = width
							}
						}
					}
					continue
				}
				w, _, err := font.SizeUTF8(chunk)
				if err != nil {
					continue
				}
				if width := int32(w) + edges + padding; width > widest {
					widest = width
				}
			}
		}
	}
	return widest
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
		// The text is drawn VerticalOffset below its row (a baseline, a
		// centred flex item, a grid track's exact edge); its box follows it.
		y := rowY + oy + run.VerticalOffset
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
	// A float still widens the boxes it sits in, as it always did here. A
	// positioned layer does not: it paints its own boxes, and CSS keeps it
	// out of its ancestors' size (webpage_layers.go).
	for _, f := range l.layout.Floats {
		if f.Phase != webLayerFloat {
			continue
		}
		for _, row := range f.Rows {
			for _, run := range row.Runs {
				addRun(run, row.Y, f.X, f.Y)
			}
		}
	}
	// Pictures are part of their boxes too: a card's surface reaches up
	// behind the photo at its top, and a figure's around its image — a
	// floated figure's as well (webpage_floats.go).
	addImage := func(im webImagePos, ox, oy int32) {
		if im.Rect.W <= 0 || im.Rect.H <= 0 {
			return
		}
		r := sdl.Rect{X: im.Rect.X + ox, Y: im.Rect.Y + oy, W: im.Rect.W, H: im.Rect.H}
		for _, id := range im.Boxes {
			span := spans[id]
			if span == nil {
				span = &containerBoxSpan{}
				spans[id] = span
			}
			if !span.seen {
				span.minX, span.minY, span.seen = r.X, r.Y, true
			}
			span.minX = min32(span.minX, r.X)
			span.minY = min32(span.minY, r.Y)
			span.maxX = max32(span.maxX, r.X+r.W)
			span.maxY = max32(span.maxY, r.Y+r.H)
		}
	}
	for _, im := range l.layout.Images {
		addImage(im, 0, 0)
	}
	for _, f := range l.layout.Floats {
		if f.Phase == webLayerFloat {
			for _, im := range f.Images {
				addImage(im, f.X, f.Y)
			}
		}
	}
	// Creation order is outer-to-inner for nested boxes, so a child's surface
	// paints over its parent's instead of being hidden by it. A layer's pass
	// paints only the boxes from its root's on (boxFloor).
	ids := make([]int, 0, len(spans))
	for id := range spans {
		if id >= l.boxFloor {
			ids = append(ids, id)
		}
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
		// The run's X starts after the wrapper's left padding/margin. The
		// painter wants the border-box edge, so back that inset out here.
		left := int32(info.style.Indent * scale)
		w := span.width
		if w <= 0 {
			// No text, only pictures: the box wraps them and its padding.
			w = span.maxX - span.minX + left + int32(info.style.IndentRight*scale)
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
		x, y := span.minX-left, span.minY-top
		// Across, the box is as wide as layout made it where it applied the
		// box (applyBoxConstraints). Its runs' union is only as wide as its
		// text: a padded card or sidebar lost its right padding that way, and
		// its lines ran over its right edge.
		if g, ok := l.layout.boxGeom[id]; ok && g.W > 0 {
			x, w = g.X, g.W
		}
		h := span.maxY - span.minY + top + bottom
		// A definite height sizes the wrapper's painted box even when its
		// children overflow it. The descendant union is only the size of an
		// auto-height wrapper; using it for a fixed-height navigation bar can
		// turn a 40px background into a full-screen panel.
		if info.style.Height > 0 {
			h = int32(math.Round(info.style.Height * scale))
			if info.style.BoxSizing == 0 {
				h += int32(math.Round((info.style.PadTop + info.style.PadBot) * scale))
			}
		}
		// min-height remains a floor for either kind of wrapper, and so does
		// the line or track a stretched flex or grid item fills (boxStretch).
		if minH := int32(math.Round(info.style.MinHeight * scale)); minH > h {
			h = minH
		}
		if s := l.layout.boxStretch[id]; s > h && info.style.Height <= 0 {
			h = s
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
		// The union is the box's own surface: tagged with its ID it paints in
		// tree order, under the boxes and pictures nested in it.
		rectStart, bgStart := len(l.layout.Rects), len(l.layout.Backgrounds)
		l.paintWebBox(info, sdl.Rect{X: x, Y: y, W: w, H: h})
		l.tagPaints(id, false, rectStart, bgStart)
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
	start := min(l.constraintSkip, len(block.Boxes))
	end := len(block.Boxes)
	nestedItemPadding := false
	if depth, ok := l.doc.FlexBoxDepth[block.FlexGroup]; ok && depth < end {
		if start < depth {
			end = depth
		} else if len(block.Runs) > 0 {
			// A flex/grid item already received its root width from the track.
			// Text inside it still needs the padding of nested wrappers. Keep
			// image and empty wrapper geometry on the existing track path.
			nestedItemPadding = true
		} else {
			end = start
		}
	}
	if end < start {
		end = start
	}
	for _, id := range block.Boxes[start:end] {
		info, ok := l.doc.Boxes[id]
		if override, found := l.boxOverride[id]; found {
			// A layer's root, whose width the layer already took.
			info, ok = override, true
		}
		if !ok || info.anchor {
			// An anchor box only names a containing block.
			continue
		}
		// A block whose style is the same element style already had its own
		// padding and width applied by the ordinary block pass. A box owner
		// with different padding is still an ancestor wrapper for this text;
		// skipping it lets an article line run beyond its card border.
		if block.BoxOwner == id && block.FloatBlocks == nil &&
			math.Abs(block.Indent-info.style.Indent) < 0.01 &&
			math.Abs(block.Style.IndentRight-info.style.IndentRight) < 0.01 {
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
		if !nestedItemPadding {
			if st.WidthPct > 0 {
				target = min32(target, int32(math.Round(float64(contentW)*st.WidthPct/100)))
			} else if st.Width > 0 {
				target = min32(target, int32(math.Round(st.Width*scale)))
			} else if fit := l.fitBoxWidth(id); fit > 0 {
				// width: fit-content: as wide as what the box holds.
				target = min32(target, fit)
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
		if free := contentW - target; !nestedItemPadding && free > 0 && (st.MarginLeftAuto || st.MarginRightAuto) {
			switch {
			case st.MarginLeftAuto && st.MarginRightAuto:
				x += free / 2
			case st.MarginLeftAuto:
				x += free
			}
		}
		// The box's border box — its margins outside, its padding inside — is
		// what the container pass paints and its positioned children are
		// placed in (paintContainerBoxes, boxFlowRect).
		marginL := min(max(int32(math.Round((st.Indent-st.PadLeft)*scale)), 0), left)
		marginR := min(max(int32(math.Round((st.IndentRight-st.PadRight)*scale)), 0), right)
		l.noteBoxGeom(id, x-left+marginL, target-marginL-marginR)
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
// Content painted inside the box (an image's frame, Inner) is not the box's
// surface and stays.
func (l *webLayouter) removeBoxPaints(id int) {
	if l == nil || l.layout == nil || id == 0 {
		return
	}
	if len(l.layout.Rects) > 0 {
		kept := l.layout.Rects[:0]
		for _, r := range l.layout.Rects {
			if r.BoxID != id || r.Inner {
				kept = append(kept, r)
			}
		}
		l.layout.Rects = kept
	}
	if len(l.layout.Backgrounds) > 0 {
		kept := l.layout.Backgrounds[:0]
		for _, bg := range l.layout.Backgrounds {
			if bg.BoxID != id || bg.Inner {
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
	scan := func(rows []webRow, images []webImagePos, ox, oy int32) {
		for _, row := range rows {
			for _, run := range row.Runs {
				if run.Font == nil || !webBoxesHave(run.Boxes, id) {
					continue
				}
				w, h, err := run.Font.SizeUTF8(run.Text)
				if err != nil {
					continue
				}
				seen = true
				x, y := run.X+ox, row.Y+oy+run.VerticalOffset
				minX = min32(minX, x)
				minY = min32(minY, y)
				maxX = max(maxX, x+int32(w))
				maxY = max(maxY, y+int32(h))
				width = max(width, run.BoxWidth)
			}
		}
		// A picture is part of the box as much as its text: a relative
		// wrapper around a photo is the containing block of the badge over
		// the photo.
		for _, im := range images {
			if im.Rect.W <= 0 || im.Rect.H <= 0 || !webBoxesHave(im.Boxes, id) {
				continue
			}
			seen = true
			minX = min32(minX, im.Rect.X+ox)
			minY = min32(minY, im.Rect.Y+oy)
			maxX = max(maxX, im.Rect.X+ox+im.Rect.W)
			maxY = max(maxY, im.Rect.Y+oy+im.Rect.H)
		}
	}
	scan(l.layout.Rows, l.layout.Images, 0, 0)
	// So are the floats it holds: a positioned figure that floats is the
	// containing block of the badge on its picture (webpage_floats.go).
	for i := range l.layout.Floats {
		if f := &l.layout.Floats[i]; f.Phase == webLayerFloat {
			scan(f.Rows, f.Images, f.X, f.Y)
		}
	}
	if !seen {
		return sdl.Rect{}, false
	}
	scale := l.baseSize / 16
	left := int32(math.Round(info.style.Indent * scale))
	top := int32(math.Round(info.style.MarginTop * scale))
	bottom := int32(math.Round(info.style.MarginBot * scale))
	if info.anchor {
		// An anchor box reserved no margins in the flow (syncBoxes).
		top, bottom = 0, 0
	}
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
		// Only pictures: the box wraps them and its padding, as it is painted.
		w = maxX - minX + left + int32(math.Round(info.style.IndentRight*scale))
	}
	if maxX > minX+w {
		w = maxX - minX
	}
	h := maxY - minY + top + bottom
	if floor := int32(math.Round(max(info.style.Height, info.style.MinHeight) * scale)); floor > h {
		h = floor
	}
	if s := l.layout.boxStretch[id]; s > h && info.style.Height <= 0 {
		// A stretched flex or grid item: the badge at its foot sits at the
		// bottom of the line, not of the item's own text.
		h = s
	}
	if w <= 0 || h <= 0 {
		return sdl.Rect{}, false
	}
	x := minX - left
	// The width layout gave the box, where it applied it: an overlay's
	// `right: 20px` is measured from the hero's right edge, its padding
	// included, not from the end of the hero's text.
	if g, ok := l.layout.boxGeom[id]; ok && g.W > 0 {
		x, w = g.X, g.W
	}
	return sdl.Rect{X: x, Y: minY - top, W: w, H: h}, true
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
		if r.BackgroundFill && r.Rect.Y+r.Rect.H > rect.H {
			r.Rect.H = max(0, rect.H-r.Rect.Y)
		}
		r.Rect.X += rect.X
		r.Rect.Y += rect.Y
		l.layout.Rects = append(l.layout.Rects, r)
	}
	for _, bg := range boxLayout.Backgrounds {
		// A shadow is the box moved by its offset: trimming it to the box
		// would cut off the part a downward offset shows below the card.
		if !bg.Shadow && bg.Rect.Y+bg.Rect.H > rect.H {
			bg.Rect.H = max(0, rect.H-bg.Rect.Y)
		}
		bg.Rect.X += rect.X
		bg.Rect.Y += rect.Y
		l.layout.Backgrounds = append(l.layout.Backgrounds, bg)
	}
}

// gridColumnSpecs expands an auto-fill/auto-fit definition to the used column
// count, or returns the explicit specs. A legacy block with only weights/bases
// is translated into a spec so the sizing path has one code path.
func (l *webLayouter) gridColumnSpecs(first webBlock, cols int) []webGridTrackSpec {
	specs := make([]webGridTrackSpec, cols)
	if first.GridAutoRepeat != 0 {
		for i := range specs {
			specs[i] = first.GridAutoTrack
		}
		return specs
	}
	for i := 0; i < cols; i++ {
		explicit := first.GridCols
		if explicit <= 0 && first.GridAreas != nil {
			explicit = first.GridAreas.Cols
		}
		if i < explicit && i < len(first.GridTrackSpecs) && first.GridTrackSpecs[i] != (webGridTrackSpec{}) {
			specs[i] = first.GridTrackSpecs[i]
			continue
		}
		if i >= explicit && first.GridAutoColCount > 0 {
			specs[i] = first.GridAutoColSpecs[(i-explicit)%first.GridAutoColCount]
			continue
		}
		if i < len(first.GridBases) && first.GridBases[i] > 0 {
			specs[i] = webGridTrackSpec{Min: first.GridBases[i], Max: first.GridBases[i]}
		}
		if i < len(first.GridWeights) && first.GridWeights[i] > 0 {
			specs[i].MaxFr = first.GridWeights[i]
		}
		if specs[i] == (webGridTrackSpec{}) {
			specs[i] = webGridTrackSpec{MinAuto: true, MaxAuto: true}
		}
	}
	return specs
}

func (l *webLayouter) gridColumnSpecPx(spec webGridTrackSpec, basis, contentMin int32) (min, maxPx int32, flexible bool) {
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	min = int32(math.Round(spec.Min * scale))
	if spec.MinPct > 0 {
		min = max(min, int32(math.Round(float64(basis)*spec.MinPct/100)))
	}
	maxPx = int32(math.Round(spec.Max * scale))
	if spec.MaxPct > 0 {
		maxPx = max(maxPx, int32(math.Round(float64(basis)*spec.MaxPct/100)))
	}
	if spec.MinAuto && contentMin > min {
		min = contentMin
	}
	if spec.MinAuto && maxPx > 0 && min > maxPx {
		min = maxPx
	}
	if spec.MaxFr > 0 || spec.MaxAuto || (spec.Max <= 0 && spec.MaxPct <= 0 && !spec.MinAuto) {
		flexible = true
	}
	if min < 0 {
		min = 0
	}
	if maxPx < min {
		maxPx = min
	}
	return min, maxPx, flexible
}

// gridColumnWidths resolves explicit tracks and minmax bounds. Fixed tracks
// reserve their definite width first; flexible tracks then share the remaining
// space in proportion to their fr factors, with each track never shrinking
// below its min. A minmax(220px, 1fr) track therefore contributes 220px before
// receiving its share, instead of being counted twice as a fixed and a
// flexible track.
func (l *webLayouter) gridColumnWidths(first webBlock, cols int, contentW, gap int32, used []bool, contentMins []int32) []int32 {
	if cols <= 0 || cols > 6 {
		return nil
	}
	// A table row and a bare grid have no authored column track list. Keep
	// their historical content/equal-column paths out of the explicit sizing
	// code; only a real template or an auto repeat needs track specs.
	if first.GridCols <= 0 && first.GridAutoRepeat == 0 && first.GridMin <= 0 && first.GridAutoColCount == 0 {
		return nil
	}
	specs := l.gridColumnSpecs(first, cols)
	hasExplicit := first.GridAutoRepeat != 0 || first.GridAutoColCount != 0
	for _, spec := range specs {
		if spec != (webGridTrackSpec{}) {
			hasExplicit = true
		}
	}
	if !hasExplicit {
		return nil
	}
	active := 0
	for i := 0; i < cols; i++ {
		if gridTrackActive(used, i) {
			active++
		}
	}
	if active == 0 {
		return make([]int32, cols)
	}
	trackSpace := contentW - gap*int32(active-1)
	if trackSpace <= 0 {
		return nil
	}
	widths := make([]int32, cols)
	mins := make([]int32, cols)
	flex := make([]bool, cols)
	fixed := int32(0)
	for i, spec := range specs {
		if !gridTrackActive(used, i) {
			continue
		}
		min, max, flexible := l.gridColumnSpecPx(spec, contentW, contentMins[i])
		mins[i], flex[i] = min, flexible
		if flexible {
			widths[i] = min
			continue
		}
		if spec.MinAuto && !flexible {
			widths[i] = min
			fixed += widths[i]
			continue
		}
		if max < min {
			max = min
		}
		widths[i] = max
		fixed += widths[i]
	}
	if fixed > trackSpace {
		// The authored minimum/definite tracks cannot fit the page measure.
		// Preserve their proportions rather than overflowing the column.
		for i := range widths {
			widths[i] = int32(float64(widths[i]) * float64(trackSpace) / float64(fixed))
			if widths[i] < 1 {
				widths[i] = 1
			}
		}
		return widths
	}
	free := trackSpace - fixed
	if free <= 0 {
		return widths
	}
	flexMin := int32(0)
	totalFr := 0.0
	for i := range specs {
		if !flex[i] || !gridTrackActive(used, i) {
			continue
		}
		flexMin += mins[i]
		totalFr += webGridTrackFr(specs[i])
	}
	if fixed+flexMin > trackSpace {
		for i := range widths {
			widths[i] = int32(float64(widths[i]) * float64(trackSpace) / float64(fixed+flexMin))
			if widths[i] < 1 {
				widths[i] = 1
			}
		}
		return widths
	}
	remaining := free - flexMin
	if remaining < 0 {
		remaining = 0
	}
	if totalFr <= 0 {
		return widths
	}
	remainingWeight := totalFr
	for i := range specs {
		if !flex[i] || !gridTrackActive(used, i) || remainingWeight <= 0 {
			continue
		}
		share := int32(math.Round(float64(remaining) * webGridTrackFr(specs[i]) / remainingWeight))
		widths[i] += share
		remaining -= share
		remainingWeight -= webGridTrackFr(specs[i])
	}
	return widths
}

func gridTrackActive(used []bool, i int) bool {
	return used == nil || (i >= 0 && i < len(used) && used[i])
}

func gridTrackOffsets(tracks []int32, gap int32, used []bool) []int32 {
	offsets := make([]int32, len(tracks)+1)
	pos := int32(0)
	for i := range tracks {
		offsets[i] = pos
		if !gridTrackActive(used, i) {
			continue
		}
		pos += tracks[i]
		for next := i + 1; next < len(tracks); next++ {
			if gridTrackActive(used, next) {
				pos += gap
				break
			}
		}
	}
	offsets[len(tracks)] = pos
	return offsets
}

func gridTrackSpan(tracks []int32, gap int32, used []bool, start, end int) int32 {
	if start < 0 {
		start = 0
	}
	if end > len(tracks) {
		end = len(tracks)
	}
	span, first := int32(0), true
	for i := start; i < end; i++ {
		if !gridTrackActive(used, i) {
			continue
		}
		if !first {
			span += gap
		}
		span += tracks[i]
		first = false
	}
	return span
}

// webGridTrackFr gives plain auto tracks a share weight so a bare `auto 1fr`
// still fills a row, while a minmax with a definite max does not.
func webGridTrackFr(spec webGridTrackSpec) float64 {
	if spec.MaxFr > 0 {
		return spec.MaxFr
	}
	if spec.MaxAuto {
		return 1
	}
	return 0
}

func (l *webLayouter) gridRowSpecs(first webBlock, rows int) []webGridTrackSpec {
	specs := make([]webGridTrackSpec, rows)
	if first.GridRowAutoRepeat != 0 {
		for i := range specs {
			specs[i] = first.GridRowAutoTrack
		}
		return specs
	}
	for i := 0; i < rows; i++ {
		explicit := first.GridRows
		if explicit <= 0 && first.GridAreas != nil {
			explicit = first.GridAreas.Rows
		}
		if i >= explicit && first.GridAutoRowCount > 0 {
			specs[i] = first.GridAutoRowSpecs[(i-explicit)%first.GridAutoRowCount]
			continue
		}
		if i < 6 {
			if first.GridRowTrackSpecs[i] != (webGridTrackSpec{}) {
				specs[i] = first.GridRowTrackSpecs[i]
				continue
			}
			if first.GridRowBases[i] > 0 {
				specs[i] = webGridTrackSpec{Min: first.GridRowBases[i], Max: first.GridRowBases[i]}
			}
			if first.GridRowWeights[i] > 0 {
				specs[i].MaxFr = first.GridRowWeights[i]
			}
		}
		if specs[i] == (webGridTrackSpec{}) {
			specs[i] = webGridTrackSpec{MinAuto: true, MaxAuto: true}
		}
	}
	return specs
}

func (l *webLayouter) gridRowSpecPx(spec webGridTrackSpec, basis int32) (min, maxPx int32, flexible, grow bool) {
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	min = int32(math.Round(spec.Min * scale))
	if spec.MinPct > 0 {
		min = max(min, int32(math.Round(float64(basis)*spec.MinPct/100)))
	}
	maxPx = int32(math.Round(spec.Max * scale))
	if spec.MaxPct > 0 {
		maxPx = max(maxPx, int32(math.Round(float64(basis)*spec.MaxPct/100)))
	}
	if maxPx < min {
		maxPx = min
	}
	flexible = spec.MaxFr > 0
	grow = spec.MaxAuto || flexible || spec.MinAuto || (maxPx > min && maxPx > 0)
	return min, maxPx, flexible, grow
}

func (l *webLayouter) gridAutoRepeatCount(spec webGridTrackSpec, available, gap int32) int {
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	basisCSS := float64(available) / scale
	definite := webGridTrackDefinitePx(spec, basisCSS)
	if definite <= 0 {
		return 1
	}
	px := int32(math.Round(definite * scale))
	if px < 1 || available < px {
		return 1
	}
	n := int((available + gap) / (px + gap))
	if n < 1 {
		n = 1
	}
	if n > 6 {
		n = 6
	}
	return n
}

// layoutGridItems lays explicit grid tracks and placements. It is separate
// from the flex row path because a grid item belongs to a named track, can
// span several tracks, and leaves empty tracks when the author started it on
// a later line. A grid also keeps its empty trailing tracks instead of
// stretching the last card across the page.
func (l *webLayouter) layoutGridItems(grouped []webFlexItem, padX, contentW int32) {
	if len(grouped) == 0 || contentW <= 0 {
		return
	}
	first := grouped[0].blocks[0]
	gap := int32(first.FlexGapX * l.baseSize / 16)
	if gap < 0 {
		gap = 0
	}
	if limit := contentW / 8; gap > limit {
		gap = limit
	}
	cols := first.GridCols
	if cols <= 0 && first.GridAutoRepeat != 0 {
		cols = l.gridAutoRepeatCount(first.GridAutoTrack, contentW, gap)
	} else if cols <= 0 && first.GridAreas != nil {
		cols = first.GridAreas.Cols
	} else if cols <= 0 && first.GridMin > 0 {
		track := int32(first.GridMin * l.baseSize / 16)
		if track < 1 {
			track = 1
		}
		if track > contentW {
			track = contentW
		}
		cols = int((contentW + gap) / (track + gap))
		if cols > len(grouped) {
			cols = len(grouped)
		}
	}
	if cols < 1 {
		cols = 1
	}
	if cols > len(first.GridWeights) {
		cols = len(first.GridWeights)
	}

	type gridPlacement struct {
		item, row, col, rowSpan, colSpan int
	}
	rowGap := int32(first.FlexGapY * l.baseSize / 16)
	if rowGap < 0 {
		rowGap = 0
	}
	explicitRows := first.GridRows
	if explicitRows <= 0 && first.GridAreas != nil {
		explicitRows = first.GridAreas.Rows
	}
	if explicitRows > len(first.GridRowBases) {
		explicitRows = len(first.GridRowBases)
	}
	occupied := make([][6]bool, 0, 2)
	ensureRow := func(row int) {
		for len(occupied) <= row {
			occupied = append(occupied, [6]bool{})
		}
	}
	fits := func(row, col, rowSpan, colSpan int) bool {
		if col < 0 || colSpan < 1 || row < 0 || rowSpan < 1 || col+colSpan > cols {
			return false
		}
		ensureRow(row + rowSpan - 1)
		for r := row; r < row+rowSpan; r++ {
			for c := col; c < col+colSpan; c++ {
				if occupied[r][c] {
					return false
				}
			}
		}
		return true
	}
	type gridCandidate struct {
		item, rowStart, rowEnd, rowSpan, colStart, colEnd, colSpan int
		area                                                       bool
	}
	candidates := make([]gridCandidate, 0, len(grouped))
	for i, item := range grouped {
		b := item.blocks[0]
		areaRowStart, areaRowEnd, areaColStart, areaColEnd := 0, 0, 0, 0
		areaOK := false
		if name := strings.TrimSpace(b.Style.GridArea); name != "" && first.GridAreas != nil {
			areaRowStart, areaRowEnd, areaColStart, areaColEnd, areaOK = first.GridAreas.bounds(name)
		}
		start := b.Style.GridColumnStart
		end := b.Style.GridColumnEnd
		span := b.Style.GridColumnSpan
		if areaOK {
			start, end, span = areaColStart, areaColEnd, 0
		}
		if span <= 0 && start > 0 && end > 0 {
			span = end - start
		}
		if span <= 0 && start > 0 && end < 0 {
			span = cols - start + 1
		}
		if start < 0 {
			start = cols + start + 1
		}
		// An explicit line past the authored tracks creates implicit columns
		// (up to the reader's cap) instead of being folded back onto the last
		// declared track.
		if start > cols && start <= 6 {
			cols = start
		}
		if start > cols {
			start = cols
		}
		if span <= 0 && start <= 0 && end > 0 {
			start = end - 1
			if start < 1 {
				start = 1
			}
			span = 1
		}
		if span <= 0 {
			span = 1
		}
		if span > 6 {
			span = 6
		}
		if start <= 0 && span > cols {
			cols = span
		}
		if start > 0 && start+span-1 > cols {
			need := start + span - 1
			if need <= 6 {
				cols = need
			} else {
				span = cols - start + 1
				if span < 1 {
					span, start = 1, cols
				}
			}
		}

		rowStart := b.Style.GridRowStart
		rowEnd := b.Style.GridRowEnd
		rowSpan := b.Style.GridRowSpan
		if areaOK {
			rowStart, rowEnd, rowSpan = areaRowStart, areaRowEnd, 0
		}
		if rowSpan <= 0 && rowStart > 0 && rowEnd > 0 {
			rowSpan = rowEnd - rowStart
		}
		if rowSpan <= 0 && rowStart > 0 && rowEnd < 0 {
			if explicitRows >= rowStart {
				rowSpan = explicitRows - rowStart + 1
			}
		}
		if rowStart < 0 {
			rowStart = explicitRows + rowStart + 1
			if rowStart < 1 {
				rowStart = 1
			}
		}
		if rowSpan <= 0 && rowStart <= 0 && rowEnd > 0 {
			rowStart = rowEnd - 1
			if rowStart < 1 {
				rowStart = 1
			}
			rowSpan = 1
		}
		if rowSpan <= 0 {
			rowSpan = 1
		}
		candidates = append(candidates, gridCandidate{
			item: i, rowStart: rowStart, rowEnd: rowEnd, rowSpan: rowSpan,
			colStart: start, colEnd: end, colSpan: span, area: areaOK,
		})
	}
	// CSS places items with an authored line before fully automatic ones: an
	// item pinned to a row (row flow) or a column (column flow) claims its cells
	// first, even when the other axis is still automatic. Without this, `grid-
	// row: 1 / 3` beside a `grid-row: span 2` sibling started on the next row.
	flowRank := func(c gridCandidate) int {
		switch {
		case c.rowStart > 0 && c.colStart > 0:
			return 0
		case first.GridAutoFlow == 1 && c.colStart > 0:
			return 1
		case first.GridAutoFlow != 1 && c.rowStart > 0:
			return 1
		case c.rowStart > 0 || c.colStart > 0:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return flowRank(candidates[i]) < flowRank(candidates[j]) })

	placements := make([]gridPlacement, 0, len(candidates))
	place := func(c gridCandidate, row, col int) {
		if row < 0 {
			row = 0
		}
		if col < 0 {
			col = 0
		}
		if c.colSpan > cols-col {
			c.colSpan = cols - col
		}
		if c.colSpan < 1 {
			c.colSpan, col = 1, cols-1
		}
		ensureRow(row + c.rowSpan - 1)
		for r := row; r < row+c.rowSpan; r++ {
			for cc := col; cc < col+c.colSpan; cc++ {
				occupied[r][cc] = true
			}
		}
		placements = append(placements, gridPlacement{item: c.item, row: row, col: col, rowSpan: c.rowSpan, colSpan: c.colSpan})
	}

	// Definite row/column pairs are placed first. This is what lets dense
	// packing (and a later auto item) see the real holes instead of treating a
	// pinned sibling as if it did not exist yet.
	for _, c := range candidates {
		if c.rowStart > 0 && c.colStart > 0 {
			place(c, c.rowStart-1, c.colStart-1)
		}
	}
	rowLimit := explicitRows
	if first.GridRowAutoRepeat != 0 && rowLimit <= 0 {
		available := int32(0)
		if first.FlexHeight > 0 {
			available = int32(math.Round(first.FlexHeight * l.baseSize / 16))
		}
		rowLimit = l.gridAutoRepeatCount(first.GridRowAutoTrack, available, rowGap)
	}
	if rowLimit < 1 {
		rowLimit = 1
	}
	// A pinned row span also establishes usable rows for column flow even
	// when the author did not declare a row template.
	for _, c := range candidates {
		if c.rowStart > 0 && c.rowStart+c.rowSpan-1 > rowLimit {
			rowLimit = c.rowStart + c.rowSpan - 1
		}
	}
	if rowLimit > 128 {
		rowLimit = 128
	}
	columnFlow := first.GridAutoFlow == 1
	if columnFlow {
		// Column flow keeps filling a column until the block axis is exhausted.
		// With no authored rows, derive enough implicit rows for every placed
		// item instead of forcing them into a single row.
		neededRows := (len(candidates) + cols - 1) / cols
		if neededRows > rowLimit {
			rowLimit = neededRows
		}
	}
	if rowLimit > 128 {
		rowLimit = 128
	}
	autoRow, autoCol := 0, 0
	for _, c := range candidates {
		if c.rowStart > 0 && c.colStart > 0 {
			continue
		}
		fixedRow, fixedCol := c.rowStart > 0, c.colStart > 0
		startRow, startCol := 0, 0
		if fixedRow {
			startRow = c.rowStart - 1
		}
		if fixedCol {
			startCol = c.colStart - 1
		} else if fixedRow {
			// A row-pinned item can start in any column of that row.
			startCol = 0
		}
		if !fixedRow && !fixedCol {
			if first.GridDense {
				startRow, startCol = 0, 0
			} else {
				startRow, startCol = autoRow, autoCol
			}
		}
		// Column flow grows implicit columns when its cursor passes the
		// authored tracks; row flow does the equivalent through implicit rows.
		if columnFlow && !fixedCol && startCol+c.colSpan > cols {
			cols = min(len(first.GridWeights), startCol+c.colSpan)
		}
		startCol = max(startCol, 0)
		row, col, found := -1, -1, false
		if columnFlow {
			for cc := startCol; cc < cols && !found; cc++ {
				if fixedCol && cc != startCol {
					continue
				}
				firstRow := 0
				if fixedRow {
					firstRow = startRow
				} else if cc == startCol {
					firstRow = startRow
				}
				for rr := firstRow; rr < rowLimit; rr++ {
					if fixedRow && rr != startRow {
						continue
					}
					if fits(rr, cc, c.rowSpan, c.colSpan) {
						row, col, found = rr, cc, true
						break
					}
				}
			}
		} else {
			for rr := startRow; rr < 128 && !found; rr++ {
				firstCol := 0
				if rr == startRow {
					firstCol = startCol
				}
				for cc := firstCol; cc+c.colSpan <= cols; cc++ {
					if fixedCol && cc != startCol {
						continue
					}
					if fixedRow && rr != startRow {
						continue
					}
					if fits(rr, cc, c.rowSpan, c.colSpan) {
						row, col, found = rr, cc, true
						break
					}
				}
			}
		}
		// Dense column flow may need a new implicit column even when its
		// search starts at column zero; grow the track list and retry the
		// new columns before falling back to the next implicit row.
		for columnFlow && !found && !fixedCol && cols < len(first.GridWeights) {
			next := cols
			cols++
			for cc := next; cc < cols && !found; cc++ {
				firstRow := 0
				if fixedRow {
					firstRow = startRow
				}
				for rr := firstRow; rr < rowLimit; rr++ {
					if fixedRow && rr != startRow {
						continue
					}
					if fits(rr, cc, c.rowSpan, c.colSpan) {
						row, col, found = rr, cc, true
						break
					}
				}
			}
		}
		if !found {
			row, col = len(occupied), 0
		}
		place(c, row, col)
		if !fixedRow && !fixedCol {
			if columnFlow {
				autoRow = row + c.rowSpan
				if autoRow >= rowLimit {
					autoRow, autoCol = 0, col+c.colSpan
				}
			} else {
				autoRow, autoCol = row, col+c.colSpan
				if autoCol >= cols {
					autoRow, autoCol = row+1, 0
				}
			}
		}
	}
	sort.SliceStable(placements, func(i, j int) bool { return placements[i].item < placements[j].item })

	var usedCols []bool
	// A grid an ad item was cut out of (webpage_adremove.go) closes the tracks
	// nothing is left in, as auto-fit does: the article beside a cut ad rail
	// takes the rail's width instead of leaving a blank column. An item across
	// every track — a header over the grid — holds none of them open.
	adCut := first.GridAutoRepeat != 2 && cols > 1 && l.doc != nil && l.doc.FlexAdCut[first.FlexGroup]
	if first.GridAutoRepeat == 2 {
		usedCols = make([]bool, cols)
		for _, p := range placements {
			for c := p.col; c < p.col+p.colSpan && c < cols; c++ {
				usedCols[c] = true
			}
		}
	} else if adCut {
		used, open := make([]bool, cols), 0
		for _, p := range placements {
			if p.colSpan >= cols {
				continue
			}
			for c := max(p.col, 0); c < p.col+p.colSpan && c < cols; c++ {
				if !used[c] {
					used[c] = true
					open++
				}
			}
		}
		if open > 0 && open < cols {
			usedCols = used
		}
	}
	// Auto minima in columns use the same intrinsic text measurement that a
	// flex item uses for its min-content floor. Distribute a spanning item's
	// floor across the tracks it covers so no single column is over-constrained.
	contentMins := make([]int32, cols)
	for _, p := range placements {
		minWidth := l.intrinsicMinContentWidth(grouped[p.item].blocks)
		if minWidth <= 0 || p.colSpan <= 0 {
			continue
		}
		per := (minWidth + int32(p.colSpan) - 1) / int32(p.colSpan)
		for c := p.col; c < p.col+p.colSpan && c < cols; c++ {
			if usedCols != nil && !usedCols[c] {
				continue
			}
			if per > contentMins[c] {
				contentMins[c] = per
			}
		}
	}
	tracks := l.gridColumnWidths(first, cols, contentW, gap, usedCols, contentMins)
	if len(tracks) == 0 {
		tracks = make([]int32, cols)
		trackW := int32(0)
		// The tracks an ad left empty are closed: the open ones share the width.
		shared := cols
		if adCut && usedCols != nil {
			shared = 0
			for _, open := range usedCols {
				if open {
					shared++
				}
			}
		}
		if shared > 0 {
			trackW = (contentW - gap*int32(shared-1)) / int32(shared)
		}
		if trackW < 1 {
			trackW = 1
		}
		for i := range tracks {
			tracks[i] = trackW
		}
	}
	// justify-content aligns the grid's column tracks inside its content box
	// when fixed tracks leave space over. fr tracks already consume that space,
	// so this is a no-op for the common fluid grid.
	gapForTracks := gap
	leading := int32(0)
	activeTracks := 0
	for i := range tracks {
		if gridTrackActive(usedCols, i) {
			activeTracks++
		}
	}
	if total := gridTrackSpan(tracks, gap, usedCols, 0, len(tracks)); total < contentW && activeTracks > 0 {
		extra := contentW - total
		switch first.FlexJustify {
		case webJustifyCenter:
			leading = extra / 2
		case webJustifyEnd:
			leading = extra
		case webJustifyBetween:
			if activeTracks > 1 {
				gapForTracks += extra / int32(activeTracks-1)
			}
		case webJustifyAround:
			per := extra / int32(activeTracks)
			gapForTracks += per
			leading = per / 2
		case webJustifyEvenly:
			per := extra / int32(activeTracks+1)
			gapForTracks += per
			leading = per
		}
	}
	offsets := gridTrackOffsets(tracks, gapForTracks, usedCols)

	type gridCell struct {
		placement gridPlacement
		sub       *webLayout
		width     int32
		contentH  int32
		align     int
		justify   int
	}
	cells := make([]gridCell, 0, len(placements))
	for _, p := range placements {
		item := grouped[p.item]
		b := item.blocks[0]
		spanW := gridTrackSpan(tracks, gap, usedCols, p.col, p.col+p.colSpan)
		if spanW < 1 {
			spanW = 1
		}
		justify := b.JustifyItems
		if b.JustifySelf >= 0 {
			justify = b.JustifySelf
		}
		itemW := l.itemWidth(b, spanW)
		// A centred or end-aligned item shrinks to its content instead of
		// filling the area, as justify-items/justify-self require. Stretch (the
		// default) keeps the full area.
		if itemW <= 0 && justify != webItemStretch {
			itemW = l.intrinsicContainerWidth(item.blocks)
			if itemW < 1 {
				itemW = spanW
			}
		}
		if itemW <= 0 || itemW > spanW {
			itemW = spanW
		}
		sub := &webLayout{RowH: l.rowH}
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, constraintSkip: l.constraintSkip, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
			spaceWidths: l.spaceWidths, sanitized: l.sanitized,
			containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan)}
		subLayouter.primeItemContainerWidths(item.blocks, itemW)
		subLayouter.layoutBlocksFlat(item.blocks, 0, itemW)
		l.absorbContainerRows(subLayouter.containerRows)
		if id := l.itemContainerID(item.blocks); id != 0 && l.baseSize > 0 {
			if size := l.containerSizes[id]; size.Height <= 0 {
				size.Height = float64(len(sub.Rows)) * float64(l.rowH) * 16 / l.baseSize
				l.containerSizes[id] = size
			}
		}
		align := b.FlexAlign
		if b.AlignSelf >= 0 {
			align = b.AlignSelf
		}
		cells = append(cells, gridCell{placement: p, sub: sub, width: itemW, contentH: int32(len(sub.Rows)) * l.rowH, align: align, justify: justify})
	}

	// Explicit row tracks reserve their authored heights even when no item
	// lands in them. Auto tracks take the tallest single-row item (or share a
	// spanning item's content need), so a dashboard row is never shorter than
	// the card it contains.
	if first.GridRowAutoRepeat != 0 && explicitRows <= 0 {
		available := int32(0)
		if first.FlexHeight > 0 {
			available = int32(math.Round(first.FlexHeight * l.baseSize / 16))
		}
		explicitRows = l.gridAutoRepeatCount(first.GridRowAutoTrack, available, rowGap)
	}
	rowCount := max(len(occupied), explicitRows)
	if rowCount < 1 {
		rowCount = 1
	}
	if rowCount > 128 {
		rowCount = 128
	}
	var usedRows []bool
	if first.GridRowAutoRepeat == 2 {
		usedRows = make([]bool, rowCount)
		for _, cell := range cells {
			for r := cell.placement.row; r < cell.placement.row+cell.placement.rowSpan && r < rowCount; r++ {
				usedRows[r] = true
			}
		}
	}
	activeRows := 0
	for i := 0; i < rowCount; i++ {
		if gridTrackActive(usedRows, i) {
			activeRows++
		}
	}
	if activeRows == 0 {
		activeRows = 1
	}
	rowSpecs := l.gridRowSpecs(first, rowCount)
	rowTracks := make([]int32, rowCount)
	autoNeed := make([]int32, rowCount)
	rowGrow := make([]bool, rowCount)
	rowFlex := make([]bool, rowCount)
	rowMax := make([]int32, rowCount)
	blockBasis := int32(0)
	if first.FlexHeight > 0 {
		blockBasis = int32(math.Round(first.FlexHeight * l.baseSize / 16))
	}
	for i := 0; i < rowCount; i++ {
		if !gridTrackActive(usedRows, i) {
			continue
		}
		spec := webGridTrackSpec{}
		if i < len(rowSpecs) {
			spec = rowSpecs[i]
		}
		min, max, flex, grow := l.gridRowSpecPx(spec, blockBasis)
		rowTracks[i], rowMax[i] = min, max
		rowGrow[i], rowFlex[i] = grow, flex
	}
	for _, cell := range cells {
		p := cell.placement
		if p.row >= rowCount {
			continue
		}
		span := min(p.rowSpan, rowCount-p.row)
		base := int32(0)
		growCount := 0
		for r := p.row; r < p.row+span; r++ {
			base += rowTracks[r]
			if rowGrow[r] {
				growCount++
			}
		}
		if growCount == 0 {
			continue
		}
		needed := cell.contentH - rowGap*int32(span-1) - base
		if needed < 0 {
			needed = 0
		}
		per := (needed + int32(growCount) - 1) / int32(growCount)
		for r := p.row; r < p.row+span; r++ {
			if !rowGrow[r] {
				continue
			}
			target := rowTracks[r] + per
			if target > autoNeed[r] {
				autoNeed[r] = target
			}
		}
	}
	for i := range rowTracks {
		if !rowGrow[i] {
			continue
		}
		target := max(rowTracks[i], autoNeed[i])
		if rowMax[i] > 0 && target > rowMax[i] {
			target = rowMax[i]
		}
		if target < rowTracks[i] {
			target = rowTracks[i]
		}
		rowTracks[i] = target
	}
	// An explicitly sized grid with fr rows can distribute its definite
	// block height over the flexible tracks, just as fr columns do. The
	// minmax floor stays in rowTracks before the free space is shared.
	if first.FlexHeight > 0 {
		totalWeight := 0.0
		fixed := int32(0)
		for i := range rowTracks {
			if rowFlex[i] {
				totalWeight += webGridTrackFr(rowSpecs[i])
			} else if i < len(first.GridRowWeights) {
				totalWeight += first.GridRowWeights[i]
			}
			fixed += rowTracks[i]
		}
		free := blockBasis - fixed - rowGap*int32(activeRows-1)
		if totalWeight > 0 && free > 0 {
			remaining := free
			remainingWeight := totalWeight
			for i := range rowTracks {
				weight := 0.0
				if rowFlex[i] {
					weight = webGridTrackFr(rowSpecs[i])
				} else if i < len(first.GridRowWeights) {
					weight = first.GridRowWeights[i]
				}
				if weight <= 0 {
					continue
				}
				add := int32(math.Round(float64(remaining) * weight / remainingWeight))
				rowTracks[i] += add
				remaining -= add
				remainingWeight -= weight
			}
		}
	}
	// align-content positions (or stretches) the whole row set inside a grid
	// container with a definite block size. Track gaps are enlarged for the
	// space-* values; stretch grows only auto-sized rows, leaving fixed and fr
	// tracks alone as the track-sizing algorithm requires.
	rowGapForTracks := rowGap
	rowLeading := int32(0)
	if first.FlexHeight > 0 {
		total := gridTrackSpan(rowTracks, rowGap, usedRows, 0, rowCount)
		if free := blockBasis - total; free > 0 {
			switch first.AlignContent {
			case webContentCenter:
				rowLeading = free / 2
			case webContentEnd:
				rowLeading = free
			case webContentBetween:
				if activeRows > 1 {
					rowGapForTracks += free / int32(activeRows-1)
				}
			case webContentAround:
				per := free / int32(activeRows)
				rowGapForTracks += per
				rowLeading = per / 2
			case webContentEvenly:
				per := free / int32(activeRows+1)
				rowGapForTracks += per
				rowLeading = per
			case webContentStretch:
				stretchRows := make([]int, 0, rowCount)
				for i := range rowTracks {
					if gridTrackActive(usedRows, i) && rowSpecs[i].MaxAuto {
						stretchRows = append(stretchRows, i)
					}
				}
				if len(stretchRows) > 0 {
					add := free / int32(len(stretchRows))
					rem := free - add*int32(len(stretchRows))
					for n, i := range stretchRows {
						rowTracks[i] += add
						if int32(n) < rem {
							rowTracks[i]++
						}
					}
				}
			}
		}
	}
	rowOffsets := gridTrackOffsets(rowTracks, rowGapForTracks, usedRows)
	gridHeight := int32(0)
	if rowCount > 0 {
		gridHeight = rowOffsets[rowCount]
	}
	if blockBasis > gridHeight {
		gridHeight = blockBasis
	}

	rowCells := make([][]int, rowCount)
	for ci := range cells {
		p := cells[ci].placement
		if p.row < rowCount {
			rowCells[p.row] = append(rowCells[p.row], ci)
		}
	}
	baseY := int32(len(l.layout.Rows)) * l.rowH
	endY := baseY + gridHeight
	for r := 0; r < rowCount; r++ {
		for _, ci := range rowCells[r] {
			cell := cells[ci]
			p := cell.placement
			b := grouped[p.item].blocks[0]
			endRow := p.row + p.rowSpan
			if endRow > rowCount {
				endRow = rowCount
			}
			areaH := gridTrackSpan(rowTracks, rowGapForTracks, usedRows, p.row, endRow)
			free := areaH - cell.contentH
			startAuto, endAuto := flexAutoCross(b)
			if free > 0 && cell.align == webItemStretch && !startAuto && !endAuto && b.Style.Height <= 0 {
				l.stretchFlexItem(cell.sub, b, cell.contentH, areaH)
			}
			drop := int32(0)
			switch {
			case startAuto && endAuto:
				drop = free / 2
			case startAuto:
				drop = free
			default:
				switch cell.align {
				case webItemCenter:
					drop = free / 2
				case webItemEnd:
					drop = free
				}
			}
			x := padX + leading + offsets[p.col]
			spanW := gridTrackSpan(tracks, gapForTracks, usedCols, p.col, p.col+p.colSpan)
			spanFree := spanW - cell.width
			startMain, endMain := flexAutoMain(b)
			switch {
			case spanFree > 0 && startMain && endMain:
				x += spanFree / 2
			case spanFree > 0 && startMain:
				x += spanFree
			case spanFree > 0 && cell.justify == webItemCenter:
				x += spanFree / 2
			case spanFree > 0 && cell.justify == webItemEnd:
				x += spanFree
			}
			y := baseY + rowLeading + rowOffsets[p.row] + drop
			l.mergeFlexSubLayoutAtY(cell.sub, x, y)
			if bottom := y + cell.contentH; bottom > endY {
				endY = bottom
			}
		}
	}
	// The row grid is the flow's clock. Add blank rows until the next ordinary
	// block starts at or below the grid's exact pixel bottom, so fixed-height
	// rows cannot make following content overlap them.
	for int32(len(l.layout.Rows))*l.rowH < endY && len(l.layout.Rows) < webMaxRows {
		l.layout.Rows = append(l.layout.Rows, webRow{Y: int32(len(l.layout.Rows)) * l.rowH})
	}
}

// stretchGridItemBackgrounds gives the grid item's own surface the full grid
// area height. A background on a child at the top of the item must retain its
// measured height (for example, a small button above several paragraphs).
func stretchGridItemBackgrounds(sub *webLayout, contentH, areaH int32) {
	if sub == nil || contentH <= 0 || areaH <= contentH {
		return
	}
	for i := range sub.Backgrounds {
		bg := &sub.Backgrounds[i]
		if bg.Rect.W > 0 && bg.Rect.H >= contentH && bg.Rect.Y == 0 && bg.Rect.H < areaH {
			bg.Rect.H = areaH
		}
	}
	for i := range sub.Rects {
		rect := &sub.Rects[i]
		if rect.BackgroundFill && rect.Rect.W > 0 && rect.Rect.H >= contentH && rect.Rect.Y == 0 && rect.Rect.H < areaH {
			rect.Rect.H = areaH
		}
	}
}

// layoutFlexRow lays one container's items out side by side: widths from
// width/flex-basis, the rest shared by flex-grow, then justify-content places
// the resulting columns.
func (l *webLayouter) layoutFlexRow(items []webBlock, padX, contentW int32) {
	l.layoutFlexRowItems(items, padX, contentW, true, 0)
}

// A nowrap row inside a fixed-height bar must keep its inline flow. Stacking
// excess items would turn an overflowing navigation strip into a tall page
// section, despite the ancestor's definite height.
func (l *webLayouter) flexInFixedHeightBox(first webBlock) bool {
	if l == nil || l.doc == nil {
		return false
	}
	for _, id := range first.Boxes {
		if info, ok := l.doc.Boxes[id]; ok && info.style.Height > 0 {
			return true
		}
	}
	return false
}

// layoutFlexRowItems is layoutFlexRow with the single-item special case made
// explicit. A one-item flex container fills its measure, but a one-item line
// produced by flex-wrap must keep that item's basis and min-content floor.
func (l *webLayouter) layoutFlexRowItems(items []webBlock, padX, contentW int32, singleFull bool, cardCap int32) {
	if len(items) == 0 {
		return
	}
	grouped := webGroupFlexItems(items)
	if len(grouped) == 0 {
		return
	}
	first := grouped[0].blocks[0]
	if first.GridCols > 0 || first.GridMin > 0 || first.GridAreas != nil ||
		first.GridAutoRepeat != 0 || first.GridRowAutoRepeat != 0 || first.GridAutoFlow != 0 || first.GridDense {
		l.layoutGridItems(grouped, padX, contentW)
		return
	}
	n := int32(len(grouped))
	gridMode := first.GridCols > 0 || first.GridMin > 0
	keepRow := !first.FlexWrap && l.flexInFixedHeightBox(first)
	autoCount := flexAutoCount(grouped)
	// A one-cell table row still has to go through the row machinery: laying
	// its only cell out flat skipped the row's own height, so a table whose
	// rows have one spanning cell advanced a row short and the next row's
	// cells painted over it.
	if singleFull && n == 1 && autoCount == 0 && first.Style.TableRole != webTableRoleCell {
		if !l.layoutFlexSingle(grouped[0], first, padX, contentW) {
			l.layoutBlocksFlat(grouped[0].blocks, padX, contentW)
		}
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
	// A run of inline-block boxes (openInlineRow) sizes each box by its
	// content, so a nav item or a button is as wide as its label.
	inlineRow := l.doc != nil && l.doc.FlexInlineRow[first.FlexGroup]
	if inlineRow {
		minCol = 24
	}
	tableRow := first.Style.TableRole == webTableRoleCell
	if tableRow {
		// A table cell is not a card: a 40px column is a legitimate width.
		minCol = 1
	}
	// A flex item's automatic minimum size is its min-content width. Measure
	// each item once, then use that floor for wrapping, shrinking and the
	// final columns; this is the same font measurement the content-sized
	// container path already uses. An explicit min-width replaces it.
	minWidths := make([]int32, len(grouped))
	if !gridMode && !tableRow {
		for i, item := range grouped {
			b := item.blocks[0]
			if !b.Style.MinWidthAuto || b.Style.MinWidth > 0 || b.Style.MinWidthPct > 0 {
				continue
			}
			floor := l.intrinsicMinContentWidth(item.blocks)
			// The automatic minimum is clamped by a smaller max-width, just
			// as the item's own used width is; otherwise a narrow badge with
			// a long word would keep a floor wider than its declared maximum.
			if maxW := webConstraintLength(b.Style.MaxWidth, b.Style.MaxWidthPct, contentW, l.baseSize/16); maxW > 0 {
				if limit := int32(math.Round(maxW * l.baseSize / 16)); floor > limit {
					floor = limit
				}
			}
			minWidths[i] = floor
		}
	}
	minFor := func(i int) int32 {
		floor := minWidths[i]
		if tableRow {
			if floor < 1 {
				floor = 1
			}
			return floor
		}
		if floor < 24 {
			floor = 24
		}
		return floor
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
			if basis <= 0 && inlineRow {
				basis = l.intrinsicContainerWidth(item.blocks)
			}
			if minWidths[i] > basis {
				basis = minWidths[i]
			}
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
			l.layoutFlexRowItems(webFlattenFlexItems(grouped[start:end]), padX, contentW, false, 0)
			start = end
		}
		return
	}
	avail := contentW - gap*(n-1)
	if !tableRow && !keepRow && avail < n*minCol {
		l.layoutBlocksFlat(webFlattenFlexItems(grouped), padX, contentW)
		return
	}

	gridWidths := l.gridColumnWidths(first, int(n), contentW, gap, nil, nil)
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
		if cardCap > 0 && w > cardCap {
			w = cardCap
		}
		if w <= 0 && containerIDs[i] != 0 && b.FlexGrow <= 0 {
			// A content-sized containment container is shrink-to-fit: its
			// own text decides the column, and cqw/cqi inside it then use that
			// measured width instead of the parser's viewport guess.
			w = l.intrinsicContainerWidth(item.blocks)
		}
		if w <= 0 && inlineRow && b.FlexGrow <= 0 {
			// An inline-block box is shrink-to-fit too; the row's free space
			// stays beside the boxes, where text-align puts it.
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
		if w > 0 || b.FlexGrow > 0 || b.FlexBasis >= 0 || b.BasisPct > 0 {
			if floor := minWidths[i]; floor > w {
				w = floor
			}
		}
		widths[i] = w
		if w > 0 {
			fixed += w
			continue
		}
		auto = append(auto, i)
	}
	// Items with no width, basis or grow of their own are as wide as their
	// content (flex-basis: auto), and justify-content places the room left
	// over: a header's logo at one end and its menu at the other, a centred
	// pair of buttons, a row of tags packed to the left. They used to share the
	// whole row in equal columns, which put a space-between menu in the
	// middle of the bar. Content wider than the row shrinks below (each item
	// in proportion to its width, never under its longest word), so a
	// paragraph beside a button gives up the room, not the button. Items the
	// reader cannot measure (a nested row, a picture of unknown size) keep the
	// equal shares.
	if len(auto) > 0 && totalGrow == 0 && !tableRow && !gridMode {
		natural := make([]int32, len(auto))
		measured := true
		for k, idx := range auto {
			w := l.shrinkToFitWidth(grouped[idx].blocks, avail)
			if w <= 0 {
				measured = false
				break
			}
			natural[k] = max(w, minWidths[idx])
		}
		if measured {
			for k, idx := range auto {
				widths[idx] = natural[k]
				fixed += natural[k]
			}
			auto = auto[:0]
		}
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
				if floor := minFor(i); widths[i]-cut < floor {
					cut = max(widths[i]-floor, 0)
				}
				widths[i] -= cut
				fixed += widths[i]
			}
			// Items that hit their min-content floor leave part of the excess
			// for the remaining items. Reclaim it once, proportionally to the
			// room still above each floor, so the row ends at or under the
			// available width instead of overflowing by the frozen share.
			if over := fixed - avail; over > 0 {
				totalCap := int32(0)
				for i := range widths {
					if cap := widths[i] - minFor(i); cap > 0 {
						totalCap += cap
					}
				}
				if totalCap > 0 {
					fixed = 0
					for i := range widths {
						cap := widths[i] - minFor(i)
						cut := int32(0)
						if cap > 0 {
							cut = int32(math.Round(float64(over) * float64(cap) / float64(totalCap)))
							if cut > cap {
								cut = cap
							}
						}
						widths[i] -= cut
						fixed += widths[i]
					}
					// Rounding can leave a few pixels; take them from the first
					// item that still has room rather than stacking the row.
					for left := fixed - avail; left > 0; {
						moved := false
						for i := range widths {
							cap := widths[i] - minFor(i)
							if cap <= 0 {
								continue
							}
							cut := min(cap, left)
							widths[i] -= cut
							left -= cut
							fixed -= cut
							moved = true
							if left == 0 {
								break
							}
						}
						if !moved {
							break
						}
					}
				}
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
				if floor := minWidths[idx]; floor > share {
					share = floor
				}
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
	if !tableRow && !keepRow && totalW > contentW {
		// A no-shrink carousel can be wider than the reader viewport. The
		// reader scrolls vertically, so flow its fixed-width cards into as
		// many columns as fit instead of shrinking every headline to dots or
		// stretching each card to a full-page row.
		fixedCards := !gridMode && len(grouped) > 1 && l.doc != nil && l.doc.FlexScrollX[first.FlexGroup]
		for i, item := range grouped {
			if item.blocks[0].FlexShrink != 0 || widths[i] <= 0 {
				fixedCards = false
				break
			}
		}
		if fixedCards {
			// At reading zoom a 400 CSS-px carousel card can consume most of
			// the panel. Keep two columns when each remains at least 280px wide.
			cardCap := contentW
			if len(grouped) >= 3 && contentW >= 560+gap {
				cardCap = (contentW - gap) / 2
			}
			start, rowW := 0, int32(0)
			addRow := func(end int) {
				if start == end {
					return
				}
				if start > 0 {
					gapRows := int(math.Ceil(first.FlexGapY * l.baseSize / 16 / float64(l.rowH)))
					for j := 0; j < min(gapRows, 3) && len(l.layout.Rows) < webMaxRows; j++ {
						l.layout.Rows = append(l.layout.Rows, webRow{Y: int32(len(l.layout.Rows)) * l.rowH})
					}
				}
				l.layoutFlexRowItems(webFlattenFlexItems(grouped[start:end]), padX, contentW, false, cardCap)
				start = end
			}
			for i, w := range widths {
				if w > cardCap {
					w = cardCap
				}
				if rowW > 0 && rowW+gap+w > contentW {
					addRow(i)
					rowW = 0
				}
				if rowW > 0 {
					rowW += gap
				}
				rowW += w
			}
			addRow(len(grouped))
			return
		}
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
		case webJustifyEvenly:
			start = free / (n + 1)
			gapExtra = start
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
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, constraintSkip: l.constraintSkip, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
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

	// The cross axis is aligned in pixels: an item is as tall as what it
	// paints (a 40px logo is 40px, not the two grid rows it reserves), the line
	// as tall as its tallest item — or as the container, when it has a
	// definite height (a 64px header bar) — and a centred item drops by half
	// the difference. The old row-quantised drop left a nav's links at the top
	// of a taller logo, and every item at the top of a fixed-height bar.
	extents := make([]int32, len(cells))
	lineH := int32(0)
	for i, cell := range cells {
		if tableRow {
			// A table cell's surface spans whole rows (webStretchTableCell).
			extents[i] = int32(len(cell.sub.Rows)) * l.rowH
		} else {
			extents[i] = webFlexCellExtent(cell.sub)
		}
		lineH = max(lineH, extents[i])
	}
	if singleFull && !tableRow && first.FlexHeight > 0 {
		lineH = max(lineH, int32(math.Round(first.FlexHeight*l.baseSize/16)))
	}
	lineRows := max(maxRows, (lineH+l.rowH-1)/l.rowH)
	baseRow := int32(len(l.layout.Rows))
	need := baseRow + lineRows
	for int32(len(l.layout.Rows)) < need && len(l.layout.Rows) < webMaxRows {
		idx := int32(len(l.layout.Rows))
		l.layout.Rows = append(l.layout.Rows, webRow{Y: idx * l.rowH})
	}

	for i, cell := range cells {
		free := max(lineH-extents[i], 0)
		drop := webFlexCrossDrop(grouped[i].blocks[0], cell.align, free)
		if crossStart, crossEnd := flexAutoCross(grouped[i].blocks[0]); !tableRow && free > 0 && cell.align == webItemStretch && !crossStart && !crossEnd {
			// align-items: stretch (the default): the item is as tall as its
			// line, so a row of cards ends level.
			l.stretchFlexItem(cell.sub, grouped[i].blocks[0], int32(len(cell.sub.Rows))*l.rowH, lineH)
		}
		if tableRow {
			// A table cell's background and borders fill the row however
			// short its content is; the content sits by its vertical-align
			// (webpage_table.go).
			webStretchTableCell(cell.sub, webTableCellKey(grouped[i].blocks[0].Style), free, drop)
			// The cell's surface keeps a padding row below its content rows,
			// which the row's own height does not count: the page has to
			// reach the surface's bottom or the last row is clipped at max
			// scroll.
			if bottom := baseRow*l.rowH + drop + webFlexCellExtent(cell.sub); bottom > l.layout.PaintBottom {
				l.layout.PaintBottom = bottom
			}
		}
		// Merge every layer the item measured, not just its text, boxes and
		// images: a card's gradient/background image, an embedded video and a
		// positioned badge all live beside the rows and used to disappear when
		// the card was a flex or grid item.
		l.mergeFlexSubLayoutShift(cell.sub, cellX[i], baseRow+drop/l.rowH, drop%l.rowH)
	}
	if tableRow && n > 0 && grouped[0].blocks[0].Style.CellLastRow {
		// A table's last row paints one padding row below the row grid
		// (webTagCellSurface). The row keeps it, so the block after the
		// table starts under the cell's bottom edge instead of on top of
		// its padding.
		if len(l.layout.Rows) < webMaxRows {
			idx := int32(len(l.layout.Rows))
			l.layout.Rows = append(l.layout.Rows, webRow{Y: idx * l.rowH})
		}
	}
}

// webFlexCrossDrop is how far down its line a flex item goes: free is the
// line's height less the item's, in pixels. Auto margins on the cross axis
// take the space first; otherwise align-self (or the container's
// align-items) decides — the start, the middle or the end.
func webFlexCrossDrop(b webBlock, align int, free int32) int32 {
	if free <= 0 {
		return 0
	}
	crossStart, crossEnd := flexAutoCross(b)
	switch {
	case crossStart && crossEnd:
		return free / 2
	case crossStart:
		return free
	case crossEnd:
		return 0
	}
	switch align {
	case webItemCenter:
		return free / 2
	case webItemEnd:
		return free
	}
	return 0
}

// webFlexCellExtent is how tall a measured flex item is in pixels: its text
// rows, surfaces and pictures. Rows it only reserves to round a picture up to
// the row grid do not count; a whole row or more past its content (a
// min-height, a padded box) does, since that is the item's own box.
func webFlexCellExtent(sub *webLayout) int32 {
	if sub == nil || sub.RowH <= 0 {
		return 0
	}
	rowsH := int32(len(sub.Rows)) * sub.RowH
	content := int32(0)
	for i, row := range sub.Rows {
		if len(row.Runs) > 0 {
			content = max(content, int32(i+1)*sub.RowH)
		}
	}
	for _, bg := range sub.Backgrounds {
		if !bg.Shadow {
			content = max(content, bg.Rect.Y+bg.Rect.H)
		}
	}
	for _, r := range sub.Rects {
		if !r.Shadow {
			content = max(content, r.Rect.Y+r.Rect.H)
		}
	}
	for _, im := range sub.Images {
		content = max(content, im.Rect.Y+im.Rect.H)
	}
	for _, v := range sub.Videos {
		content = max(content, v.Rect.Y+v.Rect.H)
	}
	if content <= 0 || rowsH-content >= sub.RowH {
		return rowsH
	}
	return content
}

// drawWebLayer paints one layer whose top-left corner is at (rect.X+f.X, top)
// on screen: backgrounds and images below its borders and text, matching the
// order of an in-flow box. Positioned layers, floats and pinned sticky copies
// share this body (webpage_layers.go); they differ only in how top is derived
// from the scroll offset. A sticky copy's run clips were measured at its flow
// place, so they are dropped (sticky).
func drawWebLayer(r *sdl.Renderer, config *Config, doc *webDocument, layout *webLayout, rect sdl.Rect, f *webFloat, top int32, sticky bool) {
	// Background layers sit under the box borders and content, exactly as
	// they do on an in-flow card. A fixed or sticky header usually carries
	// its only colour here, so dropping these left it transparent.
	for _, bg := range f.Backgrounds {
		y := top + bg.Rect.Y
		if bg.Shadow {
			drawWebShadowPaint(r, bg, rect.X+f.X+bg.Rect.X, y, rect)
			continue
		}
		if y+bg.Rect.H <= rect.Y || y >= rect.Y+rect.H || bg.Rect.W <= 0 || bg.Rect.H <= 0 {
			continue
		}
		if bg.Image != "" {
			drawWebImage(r, config, doc, webImagePos{
				Key: bg.Image, Rect: bg.Rect, ObjectFit: bg.ObjectFit,
				ObjectPosition: bg.ObjectPosition, Opacity: bg.Opacity,
				Radius: bg.Radius,
			}, rect.X+f.X+bg.Rect.X, y)
			continue
		}
		if bg.HasGradient {
			drawWebGradient(r, sdl.Rect{X: rect.X + f.X + bg.Rect.X, Y: y, W: bg.Rect.W, H: bg.Rect.H}, bg.Radius, bg.Gradient)
			continue
		}
		webPaintFill(r, rect.X+f.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, bg.Radius, bg.Color)
	}
	for _, im := range f.Images {
		if im.Invisible {
			continue
		}
		y := top + im.Rect.Y
		if im.Rect.H <= 0 || y >= rect.Y+rect.H || y+im.Rect.H <= rect.Y {
			continue
		}
		// A picture in an overflow: hidden box inside the layer is cut to it,
		// as on the page; a sticky copy's clips were measured at its flow
		// place.
		restore := func() {}
		if !sticky {
			restore = webClipPaint(r, rect, im.Clip, im.HasClip, rect.X+f.X, top)
		}
		drawWebImage(r, config, doc, im, rect.X+f.X+im.Rect.X, y)
		restore()
	}
	for _, v := range f.Videos {
		if v.Invisible {
			continue
		}
		y := top + v.Rect.Y
		if v.Rect.H <= 0 || y >= rect.Y+rect.H || y+v.Rect.H <= rect.Y {
			continue
		}
		drawWebVideo(r, config, doc, v, rect.X+f.X+v.Rect.X, y)
	}
	for _, bg := range f.Rects {
		if bg.BackgroundFill || bg.Shadow {
			// The block colour and the box shadow are already in
			// f.Backgrounds, where layers stack in CSS order; painting them
			// here again would double a translucent fill.
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
			webPaintStroke(r, rect.X+f.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 3), thickness, bg.Color)
			continue
		}
		if bg.HasGradient {
			drawWebGradient(r, sdl.Rect{X: rect.X + f.X + bg.Rect.X, Y: y, W: bg.Rect.W, H: bg.Rect.H}, webRectCorner(bg, 2), bg.Gradient)
			continue
		}
		webPaintFill(r, rect.X+f.X+bg.Rect.X, y, bg.Rect.W, bg.Rect.H, webRectCorner(bg, 2), bg.Color)
	}
	// The rows are drawn the way the page draws its own (webpage_paintfast.go):
	// joined stretches with their chips, shadows and decorations. A layer's
	// run clips are in its own coordinates.
	for i := range f.Rows {
		y := top + f.Rows[i].Y
		if y >= rect.Y+rect.H {
			break
		}
		if y+3*layout.RowH <= rect.Y {
			continue
		}
		drawWebPaintRowAt(r, config, f.paintRuns(i, sticky), rect.X+f.X, y, rect, rect.X+f.X, top)
	}
}
