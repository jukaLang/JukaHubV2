package main

// webpage_inline.go — inline images, shared baselines and inline-block rows.
//
// The reader used to make every <img> (and every inline <svg>, which is drawn
// as an image) a block of its own: README badges, emoji pictures, flags and
// the icon inside a link or a button each broke the line, and an image that
// was a whole link could not be reached with the D-pad. It also drew every run
// of a line from the line's top, so a large word beside small text, or code in
// a monospace face, sat on a different baseline. And inline-block boxes —
// nav items, pagination, tag lists, button rows — stacked one per line.
//
//   - The parser still emits an image as its own block (pages and tests read
//     doc.Blocks that way), but tags it and the text around it with one
//     InlineGroup. Layout stitches such a chain back into one paragraph whose
//     image runs are unbreakable boxes on the line (webMergeInlineChains).
//   - Every line is set on one baseline: text runs move down by the
//     difference between the line's ascent and their own, and an image stands
//     on the baseline (or follows its vertical-align).
//   - A block-level element with display: inline-block turns its parent into
//     an implicit wrapping flex row, so the flex code that already lays out
//     cards and menus sets the boxes side by side (openInlineRow).

import (
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
)

// webImageAttrSize reads an <img>'s width and height attributes in CSS px. A
// percentage width is a hint for the box, not a pixel size: it becomes the
// style's own percentage width when CSS gave none.
func webImageAttrSize(style *webStyle, attrs string) (float64, float64) {
	read := func(name string) (px, pct float64) {
		raw := strings.TrimSpace(htmlAttrValue(htmlTagAttribute(attrs, name)))
		if strings.HasSuffix(raw, "%") {
			f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, "%")), 64)
			if err != nil || math.IsNaN(f) || f <= 0 {
				return 0, 0
			}
			return 0, f
		}
		return htmlTagAttributeFloat(attrs, name), 0
	}
	w, wPct := read("width")
	h, _ := read("height")
	if wPct > 0 && style.Width <= 0 && style.WidthPct <= 0 {
		style.WidthPct = math.Min(wPct, 100)
	}
	return w, h
}

// addImageElement emits one replaced image — an <img>, or an inline <svg> the
// image pass rasterises (webpage_svg.go) — at the parser's position. It is a
// block of its own, as images always were; an inline-level one is also tied
// to the text before and after it so layout sets them as one line.
func (p *webParser) addImageElement(style webStyle, src, alt string, attrW, attrH float64) {
	inline := p.webImageInline(style)
	group, space := 0, false
	if inline {
		group = p.inlineGroup
		if group == 0 {
			p.inlineSeq++
			group = p.inlineSeq
		}
		space = p.space
		if cur := p.current; cur != nil && webBlockHasText(cur) {
			cur.InlineGroup = group
			if n := len(cur.Runs); n > 0 && strings.HasSuffix(cur.Runs[n-1].Text, " ") {
				space = true
			}
		}
	}
	p.flush()
	p.beginFlexLeaf(style)
	// A box positioned over this picture later is an overlay on a photo
	// (webpage_color.go); an icon-sized one is not a photo.
	if !(attrW > 0 && attrW <= 48 && attrH > 0 && attrH <= 48) {
		p.markImageInAnchors()
	}
	wasDirect := false
	if ids := p.boxPath(); len(ids) > 0 {
		wasDirect = p.doc.Boxes[ids[len(ids)-1]].direct
	}
	block := p.ensureBlock()
	if block.OwnsBox && !wasDirect {
		// An image is not its paragraph's surface (it paints its own frame,
		// webpage_layers.go): unless a text block owns the box, the container
		// pass paints the box around the picture.
		if info, ok := p.doc.Boxes[block.BoxOwner]; ok {
			info.direct = false
			p.doc.Boxes[block.BoxOwner] = info
		}
	}
	// The paragraph's own style, which ensureBlock gave the block, is what a
	// stitched line is laid out with; the image block itself carries the
	// image's CSS (its width, height, aspect ratio and fit).
	lineStyle := block.Style
	block.Image = true
	block.Style = style
	// ensureBlock built this block from the surrounding text's style, so a
	// positioned picture left its own insets and stacking behind and flowed
	// as an ordinary image. A page's absolutely placed logo (Wikipedia's
	// globe, `position:absolute; top:158px; left:35px` inside the wordmark
	// block) then pushed every line below it, leaving a blank band where the
	// picture should have overlapped the language grid. The image block is a
	// lone positioned box: layoutLayers places it from these fields, keeping
	// the wrapper box this block owns as its containing block
	// (webpage_layers.go), and the flow keeps its room free.
	if style.Position == webPositionAbsolute || style.Position == webPositionFixed {
		block.Float = true
		block.ZIndex = style.ZIndex
		block.PosTop, block.PosRight = style.Top, style.Right
		block.PosBottom, block.PosLeft = style.Bottom, style.Left
		block.HasTop, block.HasRight = style.HasTop, style.HasRight
		block.HasBottom, block.HasLeft = style.HasBottom, style.HasLeft
	}
	block.ImageSrc = src
	block.ImageAlt = alt
	block.ImageW, block.ImageH = attrW, attrH
	if inline {
		block.InlineGroup = group
		block.InlineSpace = space
		block.LineStyle = &lineStyle
	}
	p.flush()
	if inline {
		// The chain stays open for the text that follows; whitespace seen
		// before the image is not whitespace after it.
		p.inlineGroup = group
		p.space = false
	}
}

// webImageInline reports whether an image is inline-level: not display:block
// (or a flex/grid/table box), not floated or positioned, not a direct item of
// a flex or grid container, and not inside a form control's collected label.
func (p *webParser) webImageInline(style webStyle) bool {
	if style.DisplayBlock || style.DisplayContents || style.Display != webDisplayBlock {
		return false
	}
	if style.FloatSide != 0 || style.Position == webPositionAbsolute || style.Position == webPositionFixed {
		return false
	}
	if p.buttonCtrl != nil || p.textarea != nil || p.selectCtrl != nil {
		return false
	}
	if n := len(p.flexStack); n > 0 && p.flexDirectChild(&p.flexStack[n-1], len(p.stack)) {
		return false
	}
	return true
}

func webBlockHasText(b *webBlock) bool {
	for _, run := range b.Runs {
		if strings.TrimSpace(run.Text) != "" {
			return true
		}
	}
	return false
}

// webMergeInlineChains stitches each run of adjacent blocks that share an
// InlineGroup back into one paragraph. A lone image keeps its block layout,
// and the document's own blocks are never changed. A layer that left the line
// (an absolutely positioned badge, webParser.detachInlineLayer) and its
// static-position marker may sit between two pieces of it; they follow the
// stitched paragraph.
func webMergeInlineChains(blocks []webBlock) []webBlock {
	found := false
	for i := range blocks {
		if blocks[i].InlineGroup != 0 {
			found = true
			break
		}
	}
	if !found {
		return blocks
	}
	out := make([]webBlock, 0, len(blocks))
	var members []int
	for i := 0; i < len(blocks); {
		members = append(members[:0], i)
		if g := blocks[i].InlineGroup; g != 0 {
			for k := i + 1; k < len(blocks); k++ {
				b := &blocks[k]
				if b.InlineGroup == g && webSameInlineContext(&blocks[i], b) {
					members = append(members, k)
					continue
				}
				if b.LayerMarker == 0 && (b.Layer == 0 || b.Layer == blocks[i].Layer) {
					break
				}
			}
		}
		if len(members) < 2 {
			out = append(out, blocks[i])
			i++
			continue
		}
		chain := make([]webBlock, len(members))
		for m, k := range members {
			chain[m] = blocks[k]
		}
		out = append(out, webMergeInlineChain(chain))
		end := members[len(members)-1] + 1
		for k, m := i+1, 1; k < end; k++ {
			if m < len(members) && members[m] == k {
				m++
				continue
			}
			out = append(out, blocks[k])
		}
		i = end
	}
	return out
}

// webSameInlineContext reports whether two chain blocks were emitted in the
// same container: the same flex/grid item at every level, the same boxes and
// the same <details> state.
func webSameInlineContext(a, b *webBlock) bool {
	if a.FlexGroup != b.FlexGroup || a.FlexItem != b.FlexItem || a.Float != b.Float || a.Layer != b.Layer ||
		len(a.FlexTrail) != len(b.FlexTrail) || len(a.Boxes) != len(b.Boxes) ||
		len(a.Details) != len(b.Details) || a.DetailsShown != b.DetailsShown {
		return false
	}
	for k := range a.FlexTrail {
		if a.FlexTrail[k].group != b.FlexTrail[k].group || a.FlexTrail[k].item != b.FlexTrail[k].item {
			return false
		}
	}
	for k := range a.Boxes {
		if a.Boxes[k] != b.Boxes[k] {
			return false
		}
	}
	return true
}

// webMergeInlineChain builds the paragraph one chain stands for. Its
// block-level settings (margins, bullet, boxes, flex membership) are the
// first member's, which ensureBlock gave every member alike; an image block
// at the front contributes its paragraph style instead of its own.
func webMergeInlineChain(chain []webBlock) webBlock {
	base := chain[0]
	if base.Image {
		if base.LineStyle != nil {
			base.Style = *base.LineStyle
		}
		base.Image, base.ImageSrc, base.ImageAlt = false, "", ""
		base.ImageW, base.ImageH = 0, 0
	}
	base.InlineGroup, base.InlineSpace, base.LineStyle = 0, false, nil
	runs := make([]webRun, 0, len(chain)*2)
	for k := range chain {
		member := chain[k]
		if member.Image {
			img := member
			runs = append(runs, webRun{Style: member.Style, Image: &img})
			continue
		}
		memberRuns := member.Runs
		if k > 0 && member.InlineSpace && len(memberRuns) > 0 && !strings.HasPrefix(memberRuns[0].Text, " ") {
			// The space between the image and this text: the run loop gives a
			// leading space its gap on the line.
			memberRuns = append([]webRun(nil), memberRuns...)
			memberRuns[0].Text = " " + memberRuns[0].Text
		}
		runs = append(runs, memberRuns...)
	}
	base.Runs = runs
	return base
}

// inlineImagePos sizes one inline image for a line avail pixels wide. ok is
// false for an image with no box at all.
func (l *webLayouter) inlineImagePos(run webRun, block webBlock, avail int32, midLine bool) (webRunPos, bool) {
	img := run.Image
	if img == nil || avail <= 0 {
		return webRunPos{}, false
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	w, h := webImageDimsFromBlock(*img, l.doc, scale, avail)
	if w <= 0 || h <= 0 {
		return webRunPos{}, false
	}
	font := l.styledFont(block.Style)
	pos := webRunPos{
		Font: font, Image: img, ImageW: w, ImageH: h,
		Href: img.Style.Href, Invisible: img.Style.Invisible,
	}
	if midLine && img.InlineSpace && font != nil {
		pos.SpaceBefore = l.spaceWidth(font)
	}
	return pos, true
}

// intrinsicImageWidth is what a fixed-size image adds to a content-sized box:
// an icon, a logo or a badge in a nav item or a button row. A fluid image (a
// percentage width or max-width) adds nothing, as before — it takes the width
// it is given — and so does one whose size nothing states yet.
func (l *webLayouter) intrinsicImageWidth(b *webBlock) int32 {
	if b == nil || b.Style.WidthPct > 0 || b.Style.MaxWidthPct > 0 {
		return 0
	}
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	known := b.Style.Width > 0 || b.ImageW > 0
	if !known && l.doc != nil {
		if meta, ok := l.doc.imageMeta(b.ImageSrc); ok && meta.HasSize {
			known = true
		}
	}
	if !known {
		return 0
	}
	w, _ := webImageDimsFromBlock(*b, l.doc, scale, int32(320*scale))
	return w
}

// webLineMetrics is one line's box around its baseline, in layout pixels.
type webLineMetrics struct {
	ascent, descent         int32 // the whole line, inline images included
	textAscent, textDescent int32 // its text runs alone
}

// webAlignLineRuns sets a finished line on one baseline: each text run moves
// down by the line's ascent less its own face's, so a large word, small print
// and a monospace phrase share the baseline a browser gives them. Form
// controls keep their frames where they were. A line in one face is unchanged.
// An inline-block chip (a padded button or badge) makes the line tall enough
// for its padding and border (webpage_inlinebox.go), unless they fit within a
// quarter of a row (rowH) more than its text: the row grid is coarse, and a
// two-pixel badge padding is not worth a second row.
func webAlignLineRuns(line []webRunPos, rowH int32) webLineMetrics {
	var m webLineMetrics
	textRun := func(r *webRunPos) bool { return r.Image == nil && r.Font != nil && r.BoxW == 0 }
	for i := range line {
		r := &line[i]
		if !textRun(r) {
			continue
		}
		if a := int32(r.Font.Ascent()); a > m.textAscent {
			m.textAscent = a
		}
		if d := -int32(r.Font.Descent()); d > m.textDescent {
			m.textDescent = d
		}
	}
	m.ascent, m.descent = m.textAscent, m.textDescent
	boxAbove, boxBelow := m.ascent, m.descent
	for i := range line {
		r := &line[i]
		if textRun(r) && r.Chip != nil && r.Chip.block {
			boxAbove = max(boxAbove, int32(r.Font.Ascent())+r.Chip.padT)
			boxBelow = max(boxBelow, -int32(r.Font.Descent())+r.Chip.padB)
		}
	}
	if boxAbove+boxBelow > m.ascent+m.descent && (rowH <= 0 || boxAbove+boxBelow > rowH+rowH/4) {
		m.ascent, m.descent = boxAbove, boxBelow
	}
	// A text field is a box with its value set in the middle of it
	// (drawWebPaintRowAt), and that value stands on the line's baseline like
	// any word: a button or a label beside a tall search box lines up with the
	// box's text, not with its top edge.
	for i := range line {
		if above, below, ok := webFieldExtent(&line[i]); ok {
			m.ascent, m.descent = max(m.ascent, above), max(m.descent, below)
		}
	}
	for i := range line {
		r := &line[i]
		if r.Image == nil {
			continue
		}
		above, below := webInlineImageExtent(r, m.textAscent, m.textDescent)
		if above > m.ascent {
			m.ascent = above
		}
		if below > m.descent {
			m.descent = below
		}
	}
	for i := range line {
		r := &line[i]
		if textRun(r) {
			r.VerticalOffset += m.ascent - int32(r.Font.Ascent())
		} else if above, _, ok := webFieldExtent(r); ok {
			r.VerticalOffset += m.ascent - above
		}
	}
	return m
}

// webFieldExtent is how far a text field's box reaches above and below the
// baseline of the value drawn in its middle; ok is false for anything that
// is not a field box.
func webFieldExtent(r *webRunPos) (above, below int32, ok bool) {
	if r.Image != nil || r.Font == nil || r.BoxW <= 0 || r.BoxH <= 0 {
		return 0, 0, false
	}
	above = (r.BoxH-int32(r.Font.Height()))/2 + int32(r.Font.Ascent())
	return above, max(r.BoxH-above, 0), true
}

// webInlineImageExtent is how far an inline image reaches above and below the
// baseline under its vertical-align: baseline puts its bottom on it, middle
// centres it half an x-height above it, top and bottom align it with the text.
func webInlineImageExtent(r *webRunPos, textAscent, textDescent int32) (above, below int32) {
	h := r.ImageH
	switch r.Image.Style.VerticalAlign {
	case webVerticalMiddle:
		above = h/2 + textAscent*3/10
	case webVerticalTop, webVerticalTextTop:
		above = textAscent
	case webVerticalBottom, webVerticalTextBottom:
		above = h - textDescent
	default:
		above = h
	}
	// A negative above is an image hanging wholly below the baseline; it adds
	// nothing above the line.
	below = h - above
	if below < 0 {
		below = 0
	}
	return above, below
}

// placeInlineImages records the paint of a set line's images now that the row
// it landed on is known; top is that row's Y.
func (l *webLayouter) placeInlineImages(line []webRunPos, top int32, m webLineMetrics) {
	baseline := top + m.ascent
	for i := range line {
		r := &line[i]
		if r.Image == nil {
			continue
		}
		above, _ := webInlineImageExtent(r, m.textAscent, m.textDescent)
		l.emitImageBox(*r.Image, sdl.Rect{X: r.X, Y: baseline - above, W: r.ImageW, H: r.ImageH})
	}
}

// parentIsFlexContainer reports whether the element about to open is a direct
// child of a flex, grid or implicit inline-block row container.
func (p *webParser) parentIsFlexContainer() bool {
	n := len(p.flexStack)
	return n > 0 && p.flexStack[n-1].depth == len(p.stack)-1
}

// openInlineRow makes the parent of an inline-block element (style) a
// wrapping flex row, unless it already is one, so the element and its
// inline-block siblings are laid out side by side by the flex code, each
// with its own width, padding and surface. The row is left-, centre- or
// right-justified like the parent's text, keeps the small gap the whitespace
// between inline boxes makes, and ends with the parent or at the next
// block-level sibling (closeInlineRow). It reports whether the element is an
// item of such a row.
func (p *webParser) openInlineRow(style webStyle) bool {
	if style.FloatSide != 0 || style.Position == webPositionAbsolute || style.Position == webPositionFixed ||
		style.TableRole != webTableRoleNone {
		return false
	}
	parentAt := len(p.stack) - 1
	if parentAt < 0 {
		return false
	}
	parent := p.stack[parentAt]
	if parent.kind != "block" && parent.kind != "list" {
		return false
	}
	switch parent.style.TableRole {
	case webTableRoleNone, webTableRoleCell, webTableRoleCaption:
	default:
		return false
	}
	if n := len(p.flexStack); n > 0 && p.flexStack[n-1].depth == parentAt {
		// Already a container: a real one makes the element an item anyway,
		// an implicit one is this row.
		return p.flexStack[n-1].implicit
	}
	if n := len(p.details); n > 0 && p.details[n-1].inSummary {
		return false
	}
	p.flexSeq++
	group := p.flexSeq
	if p.doc.FlexBoxDepth == nil {
		p.doc.FlexBoxDepth = make(map[int]int)
	}
	if p.doc.FlexScrollX == nil {
		p.doc.FlexScrollX = make(map[int]bool)
	}
	// The parent's own box is already on the stack, so the boxes that belong
	// to the container are exactly the open ones (push counts a container's
	// box before pushing it).
	p.doc.FlexBoxDepth[group] = len(p.boxPath())
	p.doc.FlexScrollX[group] = false
	if p.doc.FlexInlineRow == nil {
		p.doc.FlexInlineRow = make(map[int]bool)
	}
	p.doc.FlexInlineRow[group] = true
	justify := webJustifyStart
	switch parent.style.Align {
	case 1:
		justify = webJustifyCenter
	case 2:
		justify = webJustifyEnd
	}
	p.flexStack = append(p.flexStack, webFlexFrame{
		group: group, depth: parentAt, dir: webFlexRow, justify: justify,
		align: webItemStart, alignContent: webContentStart, justifyItems: webItemStretch,
		gapX: 4, wrap: true, implicit: true,
	})
	return true
}

// closeInlineRow ends the implicit inline-block row of the current element's
// parent, so the block-level element about to open lays out full width below
// it. Text already set in the row is flushed with it.
func (p *webParser) closeInlineRow() {
	n := len(p.flexStack)
	if n == 0 {
		return
	}
	if top := p.flexStack[n-1]; top.implicit && top.depth == len(p.stack)-1 {
		p.flush()
		p.flexStack = p.flexStack[:n-1]
	}
}
