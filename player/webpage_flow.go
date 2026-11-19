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
	if sub == nil {
		return
	}
	dy := rowOffset * l.rowH
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
}

// mergeFlexSubLayoutAtY merges a measured sublayout at an exact pixel Y. The
// ordinary merge snaps to the text-row grid, which would round a 40px grid
// track up to two 29px rows; explicit grid tracks need their authored pixel
// edges, so their rows and paint layers keep the measured offset here.
func (l *webLayouter) mergeFlexSubLayoutAtY(sub *webLayout, x, y int32) {
	if sub == nil {
		return
	}
	for _, row := range sub.Rows {
		targetY := y + row.Y
		target := -1
		for i := len(l.layout.Rows) - 1; i >= 0; i-- {
			if l.layout.Rows[i].Y == targetY {
				target = i
				break
			}
			if l.layout.Rows[i].Y < targetY {
				break
			}
		}
		if target < 0 {
			target = len(l.layout.Rows)
			l.layout.Rows = append(l.layout.Rows, webRow{Y: targetY})
		}
		for _, run := range row.Runs {
			run.X += x
			if run.HasClip {
				run.Clip.X += x
				run.Clip.Y += y
			}
			l.layout.Rows[target].Runs = append(l.layout.Rows[target].Runs, run)
		}
	}
	for _, rect := range sub.Rects {
		rect.Rect.X += x
		rect.Rect.Y += y
		if rect.HasClip {
			rect.Clip.X += x
			rect.Clip.Y += y
		}
		l.layout.Rects = append(l.layout.Rects, rect)
	}
	for _, bg := range sub.Backgrounds {
		bg.Rect.X += x
		bg.Rect.Y += y
		l.layout.Backgrounds = append(l.layout.Backgrounds, bg)
	}
	for _, im := range sub.Images {
		im.Rect.X += x
		im.Rect.Y += y
		if im.HasClip {
			im.Clip.X += x
			im.Clip.Y += y
		}
		l.layout.Images = append(l.layout.Images, im)
	}
	for _, video := range sub.Videos {
		video.Rect.X += x
		video.Rect.Y += y
		if video.HasClip {
			video.Clip.X += x
			video.Clip.Y += y
		}
		l.layout.Videos = append(l.layout.Videos, video)
	}
	for _, float := range sub.Floats {
		float.X += x
		float.Y += y
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
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, constraintSkip: l.constraintSkip, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
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
		for _, run := range b.Runs {
			text := strings.TrimSpace(run.Text)
			if text == "" {
				continue
			}
			font := l.fontAt(run.Style.FontPx)
			if font == nil {
				continue
			}
			chunks := strings.Fields(text)
			if !webWhiteSpaceWraps(run.Style.WhiteSpace) || run.Style.Pre {
				chunks = strings.Split(text, "\n")
			}
			for _, chunk := range chunks {
				chunk = strings.TrimSpace(chunk)
				if chunk == "" {
					continue
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
				if width := int32(w) + padding; width > widest {
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
		// min-height remains a floor for either kind of wrapper.
		if minH := int32(math.Round(info.style.MinHeight * scale)); minH > h {
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
		if !ok {
			continue
		}
		// A block whose style is the same element style already had its own
		// padding and width applied by the ordinary block pass. A box owner
		// with different padding is still an ancestor wrapper for this text;
		// skipping it lets an article line run beyond its card border.
		if block.BoxOwner == id &&
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
		if r.BackgroundFill && r.Rect.Y+r.Rect.H > rect.H {
			r.Rect.H = max(0, rect.H-r.Rect.Y)
		}
		r.Rect.X += rect.X
		r.Rect.Y += rect.Y
		l.layout.Rects = append(l.layout.Rects, r)
	}
	for _, bg := range boxLayout.Backgrounds {
		if bg.Rect.Y+bg.Rect.H > rect.H {
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
	if first.GridAutoRepeat == 2 {
		usedCols = make([]bool, cols)
		for _, p := range placements {
			for c := p.col; c < p.col+p.colSpan && c < cols; c++ {
				usedCols[c] = true
			}
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
		if cols > 0 {
			trackW = (contentW - gap*int32(cols-1)) / int32(cols)
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
				stretchGridItemBackgrounds(cell.sub, cell.contentH, areaH)
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
	if singleFull && n == 1 && autoCount == 0 {
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
		// Find the nearest positioned ancestor before choosing a width:
		// `left` plus `right` stretch an inset overlay across that box instead
		// of leaving it at the reader's caption-sized default.
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
		autoWidth := b.WidthPct <= 0 && b.Width <= 0 && b.BasisPct <= 0 && b.FlexBasis <= 0
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
		case autoWidth && b.HasLeft && b.HasRight:
			w = anchor.W - int32(b.PosLeft*scale) - int32(b.PosRight*scale)
		case autoWidth:
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
			if autoWidth && b.HasLeft && b.HasRight {
				if w < 1 {
					w = 1
				}
			} else {
				w = 60
			}
		}
		if w > contentW && !(autoWidth && b.HasLeft && b.HasRight) {
			w = contentW
		}

		sub := &webLayout{RowH: l.rowH}
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, constraintSkip: l.constraintSkip, fontAt: l.fontAt, fontAtStyle: l.fontAtStyle,
			spaceWidths: l.spaceWidths, sanitized: l.sanitized,
			containerSizes: l.containerSizes, containerRows: make(map[int]webContainerRowSpan)}
		subLayouter.layoutBlocksFlat([]webBlock{b}, 0, w)
		l.absorbContainerRows(subLayouter.containerRows)
		if len(sub.Rows) == 0 && len(sub.Rects) == 0 && len(sub.Images) == 0 {
			continue
		}
		h := int32(len(sub.Rows)) * l.rowH
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
