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

	"github.com/veandco/go-sdl2/sdl"
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
	// flex-basis overrides width when both are present. A zero basis stays
	// zero here and receives its share through flex-grow below.
	if b.BasisPct > 0 {
		return int32(float64(avail) * b.BasisPct / 100)
	}
	if b.FlexBasis >= 0 {
		return int32(b.FlexBasis * scale)
	}
	if b.WidthPct > 0 {
		return int32(float64(avail) * b.WidthPct / 100)
	}
	if b.Width > 0 {
		return int32(b.Width * scale)
	}
	return 0
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
		w := l.itemWidth(b, avail)
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
		// The author's widths do not fit. Scale them down together so the row
		// still reads as a row instead of pushing the last card off the page.
		shrink := float64(avail) / float64(fixed)
		fixed = 0
		for i := range widths {
			if widths[i] <= 0 {
				continue
			}
			widths[i] = int32(float64(widths[i]) * shrink)
			if widths[i] < 24 {
				widths[i] = 24
			}
			fixed += widths[i]
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
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt,
			spaceWidths: l.spaceWidths, sanitized: l.sanitized}
		subLayouter.layoutBlocksFlat(item.blocks, 0, widths[i])
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
		subLayouter := &webLayouter{doc: l.doc, layout: sub, rowH: l.rowH, baseSize: l.baseSize, fontAt: l.fontAt,
			spaceWidths: l.spaceWidths, sanitized: l.sanitized}
		subLayouter.layoutBlocksFlat([]webBlock{b}, 0, w)
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
