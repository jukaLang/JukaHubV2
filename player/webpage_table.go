package main

import (
	"sort"

	"github.com/veandco/go-sdl2/sdl"
)

// webpage_table.go — the table formatting pass.
//
// The HTML parser emits table cells as ordinary blocks tagged with a TableID,
// TableRow and table role. This pass turns those cells into a column grid:
// rowspans claim slots, colspans widen a cell across tracks, table-layout
// fixed/auto resolves the tracks, and border-collapse removes doubled interior
// edges before the ordinary row flex pass draws the cells.
//
// Keeping this out of the flex algorithm is deliberate. Flex is about the
// container's children; a table's columns are shared by rows that are separate
// flex containers, and a rowspan is a fact only the whole grid knows.

const (
	webTableMaxColumns = 32
	webTableMinCell    = 8
)

type webTablePlan struct {
	id       int
	table    webBlock
	rows     map[int][]webBlock
	rowOrder []int
	items    map[int][]webBlock
	target   int32
	cols     *webTableColumns
	rowCount int
}

type webTableColumn struct {
	width    float64
	widthPct float64
	span     int
}

// webTableColumns is the parse-time table metadata shared by every cell: the
// left-to-right <col>/<colgroup> definitions plus table and row border styles.
// A pointer keeps webStyle comparable and avoids copying a column list onto
// every cell in a large table.
type webTableColumns struct {
	defs  []webTableColumn
	table webTableBorderSpec
	rows  map[int]webTableBorderSpec
}

type webTableBorderEdge struct {
	width    float64
	style    int
	color    sdl.Color
	hasColor bool
	valid    bool
	source   int
}

type webTableBorderSpec [4]webTableBorderEdge

// prepareWebTables rewrites the cell blocks of every table into columns. It is
// a no-op for pages without table roles, so ordinary documents pay one scan.
func (l *webLayouter) prepareWebTables(blocks []webBlock, contentW int32) []webBlock {
	if len(blocks) == 0 || l.baseSize <= 0 {
		return blocks
	}
	plans := make(map[int]*webTablePlan)
	for _, block := range blocks {
		if block.Style.TableRole != webTableRoleTable || block.Style.TableID == 0 {
			continue
		}
		plans[block.Style.TableID] = &webTablePlan{
			id: block.Style.TableID, table: block, cols: block.Style.TableCols,
			rows: make(map[int][]webBlock), items: make(map[int][]webBlock),
		}
	}
	for _, block := range blocks {
		if block.Style.TableRole != webTableRoleCell || block.Style.TablePrepared {
			continue
		}
		plan := plans[block.Style.TableID]
		if plan == nil {
			// A table whose only content is cells never emitted a block of its
			// own (the parser has no empty table block). Its cells still carry
			// the table width and layout settings inherited from the context.
			plan = &webTablePlan{
				id: block.Style.TableID, table: webBlock{Style: block.Style}, cols: block.Style.TableCols,
				rows: make(map[int][]webBlock), items: make(map[int][]webBlock),
			}
			plans[block.Style.TableID] = plan
		}
		if plan.cols == nil {
			plan.cols = block.Style.TableCols
		}
		row := block.Style.TableRow
		if _, ok := plan.rows[row]; !ok {
			plan.rowOrder = append(plan.rowOrder, row)
		}
		plan.rows[row] = append(plan.rows[row], block)
		if row+1 > plan.rowCount {
			plan.rowCount = row + 1
		}
	}
	for _, block := range blocks {
		if !block.TableEmptyRow {
			continue
		}
		if plan := plans[block.Style.TableID]; plan != nil {
			if block.Style.TableRow+1 > plan.rowCount {
				plan.rowCount = block.Style.TableRow + 1
			}
			if plan.cols == nil && len(plan.rows) == 0 && block.Style.TableCols != nil {
				plan.cols = block.Style.TableCols
			}
		}
	}
	if len(plans) == 0 {
		return blocks
	}
	for _, plan := range plans {
		l.prepareWebTable(plan, contentW)
	}

	out := make([]webBlock, 0, len(blocks))
	emitted := make(map[[2]int]bool)
	for _, block := range blocks {
		if block.TableEmptyRow {
			if plan := plans[block.Style.TableID]; plan != nil && len(plan.rows[block.Style.TableRow]) > 0 {
				continue
			}
			out = append(out, block)
			continue
		}
		if block.Style.TableRole != webTableRoleCell {
			out = append(out, block)
			continue
		}
		plan := plans[block.Style.TableID]
		if plan == nil {
			out = append(out, block)
			continue
		}
		row := block.Style.TableRow
		key := [2]int{block.Style.TableID, row}
		if emitted[key] {
			continue
		}
		emitted[key] = true
		out = append(out, plan.items[row]...)
	}
	return out
}

func (l *webLayouter) prepareWebTable(plan *webTablePlan, contentW int32) {
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	sort.Ints(plan.rowOrder)
	if len(plan.rowOrder) == 0 {
		return
	}
	rows := plan.rowCount
	if rows < len(plan.rowOrder) {
		rows = len(plan.rowOrder)
	}
	if rows < 1 {
		rows = 1
	}
	cols := 0
	occ := make([][]bool, rows)
	for r := range occ {
		occ[r] = make([]bool, 0, 8)
	}
	// Place every source cell at the first free slot in its row. A rowspan
	// from above reserves slots here, which is why later rows do not shift a
	// cell left into the space the span still occupies.
	for _, r := range plan.rowOrder {
		if r < 0 || r >= rows {
			continue
		}
		row := r
		for ci := range plan.rows[row] {
			cell := &plan.rows[row][ci]
			if cell.Style.ColSpan < 1 {
				cell.Style.ColSpan = 1
			}
			if cell.Style.RowSpan < 1 {
				cell.Style.RowSpan = 1
			}
			if cell.Style.ColSpan > webTableMaxColumns {
				cell.Style.ColSpan = webTableMaxColumns
			}
			if cell.Style.RowSpan > rows-r {
				cell.Style.RowSpan = rows - r
			}
			c := 0
			for c < len(occ[r]) && occ[r][c] {
				c++
			}
			if c >= webTableMaxColumns {
				c = webTableMaxColumns - 1
			}
			span := cell.Style.ColSpan
			if c+span > webTableMaxColumns {
				span = webTableMaxColumns - c
				cell.Style.ColSpan = span
			}
			if c+span > cols {
				cols = c + span
			}
			cell.Style.TableCol = c
			for rr := r; rr < r+cell.Style.RowSpan && rr < rows; rr++ {
				for cc := c; cc < c+span; cc++ {
					for len(occ[rr]) <= cc {
						occ[rr] = append(occ[rr], false)
					}
					occ[rr][cc] = true
				}
			}
		}
	}
	if plan.cols != nil {
		colCount := 0
		for _, def := range plan.cols.defs {
			span := def.span
			if span < 1 {
				span = 1
			}
			if span > webTableMaxColumns {
				span = webTableMaxColumns
			}
			colCount += span
		}
		if colCount > cols {
			cols = colCount
		}
	}
	if cols < 1 {
		cols = 1
	}
	if cols > webTableMaxColumns {
		cols = webTableMaxColumns
	}
	track, target := l.webTableTracks(plan, cols, contentW, scale)
	plan.target = target
	if plan.table.Style.BorderCollapse == webBorderCollapseCollapse {
		webTableResolveCollapse(plan, rows, cols)
	}
	for _, r := range plan.rowOrder {
		row := r
		items := make([]webBlock, 0, cols)
		for c := 0; c < cols; c++ {
			if cell, ok := webTableCellAt(plan.rows[row], c); ok {
				cell = webTableSizeCell(cell, track, scale)
				cell.FlexGapX = cell.Style.TableSpacingX
				if r > 0 {
					cell.MarginTop = cell.Style.TableSpacingY
				}
				cell.Style.TableColumnCount = cols
				cell.Style.TableWidthPx = float64(target) / scale
				cell.Style.TableWidthPct = 0
				cell.FlexItem = 0
				cell.FlexDir = webFlexRow
				cell.FlexTrail = nil
				items = append(items, cell)
				c += cell.Style.ColSpan - 1
				continue
			}
			// A rowspan from an earlier row leaves a hole here. An empty block
			// still has to be a flex item or the columns after it slide left.
			placeholder := webTablePlaceholder(plan.rows[row], r, c, track[c], scale)
			placeholder.FlexGapX = placeholder.Style.TableSpacingX
			if r > 0 {
				placeholder.MarginTop = placeholder.Style.TableSpacingY
			}
			items = append(items, placeholder)
		}
		plan.items[row] = items
	}
}

func webTableCellAt(cells []webBlock, col int) (webBlock, bool) {
	for _, cell := range cells {
		if cell.Style.TableCol == col {
			return cell, true
		}
	}
	return webBlock{}, false
}

func webTableSizeCell(cell webBlock, track []int32, scale float64) webBlock {
	span := cell.Style.ColSpan
	if span < 1 {
		span = 1
	}
	width := int32(0)
	for c := cell.Style.TableCol; c < cell.Style.TableCol+span && c < len(track); c++ {
		width += track[c]
	}
	if width < 1 {
		width = 1
	}
	cell.Width = float64(width) / scale
	cell.WidthPct = 0
	cell.Style.Width = cell.Width
	cell.Style.WidthPct = 0
	cell.Style.BasisPct = 0
	cell.Style.FlexBasis = -1
	cell.Style.FlexGrow = 0
	cell.Style.FlexShrink = 0
	cell.Style.TablePrepared = true
	return cell
}

func webTablePlaceholder(row []webBlock, rowIndex, col int, width int32, scale float64) webBlock {
	st := webStyle{
		Display: webDisplayTableCell, TableRole: webTableRoleCell,
		TableRow: rowIndex, TableCol: col, ColSpan: 1, RowSpan: 1,
	}
	if len(row) > 0 {
		st = row[0].Style
		st.TableRole = webTableRoleCell
		st.TableRow = rowIndex
		st.TableCol = col
		st.ColSpan, st.RowSpan = 1, 1
		st.Display = webDisplayTableCell
		st.HasBg, st.HasGradient = false, false
		st.BoxBorder = false
		st.BorderLeft, st.BorderRight = false, false
		st.BorderTop, st.BorderBottom = false, false
		st.BorderSideWidth = [4]float64{}
		st.BorderSideStyle = [4]int{}
		st.BorderSideStyleSet = [4]bool{}
		st.BorderSideColor = [4]sdl.Color{}
		st.BorderSideHasColor = [4]bool{}
		st.FlexGrow, st.FlexShrink, st.FlexBasis = 0, 0, -1
		st.BasisPct, st.WidthPct = 0, 0
		st.FontPx = 0
	}
	b := webBlock{Style: st}
	b.FlexGroup = 0
	if len(row) > 0 {
		b.FlexGroup = row[0].FlexGroup
	}
	b.FlexItem = 0
	b.FlexDir = webFlexRow
	b.Width = float64(width) / scale
	b.Style.Width = b.Width
	b.Style.WidthPct = 0
	b.Style.FlexBasis = -1
	b.Style.FlexGrow, b.Style.FlexShrink = 0, 0
	b.Style.TablePrepared = true
	return b
}

// webTableTracks resolves the column widths. Fixed reads only the first row's
// explicit widths (a cell without one shares the remaining width); auto takes
// the widest content demand per column and then distributes spare room.
func (l *webLayouter) webTableTracks(plan *webTablePlan, cols int, contentW int32, scale float64) ([]int32, int32) {
	track := make([]int32, cols)
	target := contentW
	if plan.table.Style.TableWidthPct > 0 {
		target = int32(float64(contentW) * plan.table.Style.TableWidthPct / 100)
	} else if plan.table.Style.TableWidthPx > 0 {
		target = int32(plan.table.Style.TableWidthPx * scale)
	} else if plan.table.Style.WidthPct > 0 {
		target = int32(float64(contentW) * plan.table.Style.WidthPct / 100)
	} else if plan.table.Style.Width > 0 {
		target = int32(plan.table.Style.Width * scale)
	}
	if target < 32 {
		target = 32
	}
	if target > contentW {
		target = contentW
	}
	fixed := plan.table.Style.TableLayout == webTableLayoutFixed
	colSpecified := make([]bool, cols)
	if plan.cols != nil {
		c := 0
		for _, def := range plan.cols.defs {
			if c >= cols {
				break
			}
			span := def.span
			if span < 1 {
				span = 1
			}
			if span > cols-c {
				span = cols - c
			}
			width := int32(0)
			if def.widthPct > 0 {
				width = int32(float64(target) * def.widthPct / 100)
			} else if def.width > 0 {
				width = int32(def.width * scale)
			}
			if width < 0 {
				width = 0
			}
			if width > target {
				width = target
			}
			for j := 0; j < span; j++ {
				if width > 0 {
					track[c+j] = width
					colSpecified[c+j] = true
				}
			}
			c += span
		}
	}
	explicit := make([]bool, cols)
	if fixed {
		first := plan.rowOrder[0]
		for _, cell := range plan.rows[first] {
			width := webTableExplicitWidth(cell, target, scale)
			if width <= 0 {
				continue
			}
			span := cell.Style.ColSpan
			if span < 1 {
				span = 1
			}
			each := width / int32(span)
			for c := cell.Style.TableCol; c < cell.Style.TableCol+span && c < cols; c++ {
				track[c] = each
				explicit[c] = true
			}
		}
		// Columns the first row did not size share what is left equally.
		remaining := target
		unsized := 0
		for c := 0; c < cols; c++ {
			if explicit[c] {
				remaining -= track[c]
			} else if colSpecified[c] {
				remaining -= track[c]
			} else {
				unsized++
			}
		}
		if unsized > 0 {
			if remaining < int32(unsized)*webTableMinCell {
				remaining = int32(unsized) * webTableMinCell
			}
			share := remaining / int32(unsized)
			for c := 0; c < cols; c++ {
				if !explicit[c] && !colSpecified[c] {
					track[c] = share
				}
			}
		}
	} else {
		for _, row := range plan.rowOrder {
			for _, cell := range plan.rows[row] {
				demand := l.intrinsicContainerWidth([]webBlock{cell})
				if demand <= 0 {
					demand = webTableMinCell * int32(scale)
				}
				if width := webTableExplicitWidth(cell, target, scale); width > demand {
					demand = width
				}
				span := cell.Style.ColSpan
				if span < 1 {
					span = 1
				}
				have := int32(0)
				for c := cell.Style.TableCol; c < cell.Style.TableCol+span && c < cols; c++ {
					have += track[c]
				}
				if demand > have {
					add := (demand - have + int32(span) - 1) / int32(span)
					for c := cell.Style.TableCol; c < cell.Style.TableCol+span && c < cols; c++ {
						track[c] += add
					}
				}
			}
		}
		for c := 0; c < cols; c++ {
			if track[c] < int32(webTableMinCell*scale) {
				track[c] = int32(webTableMinCell * scale)
			}
		}
	}

	sum := int32(0)
	for _, w := range track {
		sum += w
	}
	if sum < target {
		room := target - sum
		for c := 0; c < cols && room > 0; c++ {
			add := room / int32(cols-c)
			track[c] += add
			room -= add
		}
	} else if sum > target {
		// Shrink proportionally so a fixed table never paints past its column.
		for c := 0; c < cols; c++ {
			track[c] = track[c] * target / sum
			if track[c] < 1 {
				track[c] = 1
			}
		}
	}
	return track, target
}

func webTableExplicitWidth(cell webBlock, target int32, scale float64) int32 {
	if cell.Style.WidthPct > 0 {
		if w := int32(float64(target) * cell.Style.WidthPct / 100); w > 0 {
			return w
		}
	}
	if cell.Style.Width > 0 {
		return int32(cell.Style.Width * scale)
	}
	if cell.Width > 0 {
		return int32(cell.Width * scale)
	}
	return 0
}

func webTableBorderSpecFromStyle(style webStyle, source int) webTableBorderSpec {
	var spec webTableBorderSpec
	flags := [4]bool{
		style.BorderLeft || style.BoxBorder,
		style.BorderRight || style.BoxBorder,
		style.BorderTop || style.BoxBorder,
		style.BorderBottom || style.BoxBorder,
	}
	for i, present := range flags {
		if !present {
			continue
		}
		width := style.BorderSideWidth[i]
		if width <= 0 {
			width = style.BorderWidth
		}
		if width < 0 {
			width = 0
		}
		if width > 32 {
			width = 32
		}
		edge := webTableBorderEdge{width: width, style: style.BorderStyle, color: style.BorderColor,
			hasColor: style.HasBorderColor, valid: true, source: source}
		if style.BorderSideStyleSet[i] {
			edge.style = style.BorderSideStyle[i]
		}
		if style.BorderSideHasColor[i] {
			edge.color, edge.hasColor = style.BorderSideColor[i], true
		}
		spec[i] = edge
	}
	return spec
}

func webTableBorderSpecFromBlock(block webBlock, source int) webTableBorderSpec {
	var spec webTableBorderSpec
	flags := [4]bool{
		block.BorderLeft || block.BoxBorder,
		block.BorderRight || block.BoxBorder,
		block.BorderTop || block.BoxBorder,
		block.BorderBottom || block.BoxBorder,
	}
	for i, present := range flags {
		if !present {
			continue
		}
		width := block.BorderSideWidth[i]
		if width <= 0 {
			width = block.BorderWidth
		}
		if width < 0 {
			width = 0
		}
		if width > 32 {
			width = 32
		}
		edge := webTableBorderEdge{width: width, style: block.BorderStyle, color: block.BorderColor,
			hasColor: block.HasBorderColor, valid: true, source: source}
		if block.BorderSideStyleSet[i] {
			edge.style = block.BorderSideStyle[i]
		}
		if block.BorderSideHasColor[i] {
			edge.color, edge.hasColor = block.BorderSideColor[i], true
		}
		spec[i] = edge
	}
	return spec
}

func webTableBorderPriority(style int) int {
	switch style {
	case webBorderHidden:
		return 6
	case webBorderDouble:
		return 5
	case webBorderSolid:
		return 4
	case webBorderDashed:
		return 3
	case webBorderDotted:
		return 2
	case webBorderNone:
		return 1
	}
	return 0
}

func webTableBorderWinner(a, b webTableBorderEdge) webTableBorderEdge {
	if !a.valid {
		return b
	}
	if !b.valid {
		return a
	}
	if a.width != b.width {
		if a.width > b.width {
			return a
		}
		return b
	}
	if pa, pb := webTableBorderPriority(a.style), webTableBorderPriority(b.style); pa != pb {
		if pa > pb {
			return a
		}
		return b
	}
	if a.source >= b.source {
		return a
	}
	return b
}

// webTableResolveCollapse resolves each boundary once. A vertical boundary is
// owned by the cell on its left, a horizontal one by the cell above, so the
// winning edge is painted exactly once. Winners compare width, then style
// priority (hidden > double > solid > dashed > dotted > none), then cell over
// row over table, which is the CSS border-conflict order.
func webTableResolveCollapse(plan *webTablePlan, rows, cols int) {
	if plan == nil || rows < 1 || cols < 1 {
		return
	}
	grid := make([][]*webBlock, rows)
	for r := range grid {
		grid[r] = make([]*webBlock, cols)
	}
	for _, rowIndex := range plan.rowOrder {
		for i := range plan.rows[rowIndex] {
			cell := &plan.rows[rowIndex][i]
			span, rspan := cell.Style.ColSpan, cell.Style.RowSpan
			if span < 1 {
				span = 1
			}
			if rspan < 1 {
				rspan = 1
			}
			for rr := cell.Style.TableRow; rr < cell.Style.TableRow+rspan && rr < rows; rr++ {
				for cc := cell.Style.TableCol; cc < cell.Style.TableCol+span && cc < cols; cc++ {
					grid[rr][cc] = cell
				}
			}
		}
	}
	tableSpec := webTableBorderSpec{}
	if plan.cols != nil {
		tableSpec = plan.cols.table
	}
	for _, rowIndex := range plan.rowOrder {
		if rowIndex < 0 || rowIndex >= rows {
			continue
		}
		rowSpec := webTableBorderSpec{}
		if plan.cols != nil {
			rowSpec = plan.cols.rows[rowIndex]
		}
		for i := range plan.rows[rowIndex] {
			cell := &plan.rows[rowIndex][i]
			r, c := cell.Style.TableRow, cell.Style.TableCol
			if r < 0 || r >= rows || c < 0 || c >= cols {
				continue
			}
			span, rspan := cell.Style.ColSpan, cell.Style.RowSpan
			if span < 1 {
				span = 1
			}
			if rspan < 1 {
				rspan = 1
			}
			own := webTableBorderSpecFromBlock(*cell, 2)
			edges := [4]webTableBorderEdge{own[0], own[1], own[2], own[3]}
			// Left and right boundaries.
			for side := 0; side <= 1; side++ {
				if side == 0 && c > 0 {
					// The cell on the left owns the interior vertical edge.
					edges[side].valid = false
					continue
				}
				if side == 1 && c+span >= cols {
					edges[side] = webTableBorderWinner(edges[side], tableSpec[side])
					edges[side] = webTableBorderWinner(edges[side], rowSpec[side])
					continue
				}
				neighborCol := c - 1
				if side == 1 {
					neighborCol = c + span
				}
				if neighborCol < 0 || neighborCol >= cols {
					edges[side] = webTableBorderWinner(edges[side], tableSpec[side])
				} else if n := grid[r][neighborCol]; n != nil && n != cell {
					other := webTableBorderSpecFromBlock(*n, 2)
					opposite := 0
					if side == 0 {
						opposite = 1
					} else {
						opposite = 0
					}
					edges[side] = webTableBorderWinner(edges[side], other[opposite])
				}
				edges[side] = webTableBorderWinner(edges[side], rowSpec[side])
			}
			// Top and bottom boundaries.
			for side := 2; side <= 3; side++ {
				if side == 2 && r > 0 {
					// The cell above owns the interior horizontal edge.
					edges[side].valid = false
					continue
				}
				if side == 3 && r+rspan >= rows {
					edges[side] = webTableBorderWinner(edges[side], tableSpec[side])
					edges[side] = webTableBorderWinner(edges[side], rowSpec[side])
					continue
				}
				neighborRow := r - 1
				if side == 3 {
					neighborRow = r + rspan
				}
				if neighborRow < 0 || neighborRow >= rows {
					edges[side] = webTableBorderWinner(edges[side], tableSpec[side])
				} else if n := grid[neighborRow][c]; n != nil && n != cell {
					other := webTableBorderSpecFromBlock(*n, 2)
					opposite := 3
					if side == 3 {
						opposite = 2
					}
					edges[side] = webTableBorderWinner(edges[side], other[opposite])
				}
				edges[side] = webTableBorderWinner(edges[side], rowSpec[side])
			}
			cell.CollapseEdges = edges
			cell.HasCollapseEdges = true
			cell.BoxBorder, cell.Style.BoxBorder = false, false
			cell.BorderLeft, cell.BorderRight = false, false
			cell.BorderTop, cell.BorderBottom = false, false
			cell.Style.BorderLeft, cell.Style.BorderRight = false, false
			cell.Style.BorderTop, cell.Style.BorderBottom = false, false
		}
	}
}

// layoutPlaceholder reserves a box for a replaced element the reader cannot
// rasterize. The box carries the element's resolved background and border, so
// the rest of the page wraps around it instead of reading the fallback markup
// as prose.
func (l *webLayouter) layoutPlaceholder(b webBlock, padX, contentW int32) {
	layout, rowH := l.layout, l.rowH
	if layout == nil || rowH <= 0 || contentW <= 0 {
		return
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	w := contentW
	if b.Style.WidthPct > 0 {
		w = int32(float64(contentW) * b.Style.WidthPct / 100)
	} else if b.Style.Width > 0 {
		w = int32(b.Style.Width * scale)
	}
	if w < 1 {
		w = int32(300 * scale)
	}
	if w > contentW {
		w = contentW
	}
	h := int32(150 * scale)
	if b.Style.Height > 0 {
		h = int32(b.Style.Height * scale)
	}
	if h < rowH {
		h = rowH
	}
	x := padX
	switch b.Align {
	case 1:
		x += (contentW - w) / 2
	case 2:
		x += contentW - w
	}
	if x < padX {
		x = padX
	}
	y := int32(len(layout.Rows)) * rowH
	if !b.Style.Invisible {
		if b.HasBg {
			layout.Rects = append(layout.Rects, webRect{Rect: sdl.Rect{X: x, Y: y, W: w, H: h}, Color: b.Background, Z: b.ZIndex})
		}
		if b.BoxBorder || b.BorderLeft || b.BorderRight || b.BorderTop || b.BorderBottom {
			color := sdl.Color{R: 170, G: 178, B: 192, A: 255}
			if b.HasBorderColor {
				color = b.BorderColor
			}
			thickness := int32(b.BorderWidth)
			if thickness <= 0 {
				thickness = 1
			}
			layout.Rects = append(layout.Rects, webRect{
				Rect: sdl.Rect{X: x, Y: y, W: w, H: h}, Color: color, Stroke: true,
				Thickness: thickness, Z: b.ZIndex, BorderStyle: b.BorderStyle,
			})
		}
	}
	slots := (h + rowH - 1) / rowH
	if slots < 1 {
		slots = 1
	}
	for i := int32(0); i < slots && len(layout.Rows) < webMaxRows; i++ {
		layout.Rows = append(layout.Rows, webRow{Y: y + i*rowH})
	}
}
