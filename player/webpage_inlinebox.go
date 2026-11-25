package main

// webpage_inlinebox.go — inline boxes: the padding, border and margin of an
// inline element, and the background chip drawn around its text.
//
// A button written as `<a class="btn">` (or a badge, a tag, a pill, an inline
// <code> on GitHub) is an inline element with padding, a background, a border
// and a radius. The reader used to draw it as its bare text on a tight
// rectangle with fixed 3px corners: the padding took no room (a single space
// stood in for it), the background did not reach over it, the border and the
// author's radius were not drawn, and a padded inline-block button squeezed
// into a text row. Now:
//
//   - the left edge of an inline box — its margin, border and padding — is
//     room before its first word, and its right edge room after its last
//     (webRun.Lead/Trail, set by the parser as the element opens and closes),
//     so the line measures and wraps the box as a browser does;
//   - an inline-block box (display: inline-block / inline-flex, as buttons
//     and badges usually are) is atomic: the line may break at its edges even
//     with no space there, and it moves to the next line whole rather than
//     leave its first word behind (CSS Text 3 §5.1);
//   - the chip covers the padding and border on all four sides, takes the
//     element's border-radius (a pill stays a pill) and its border, and runs
//     on unbroken under the elements and generated content inside the box;
//   - an inline-block box makes its line tall enough for its vertical padding
//     and border once they no longer fit the row's own spacing, while a plain
//     inline box's padding overlaps the lines around it, as in CSS.
//
// A submit button (<button>, <input type=submit>) is the same box: its padding
// and border are the button's, and an unstyled one gets the browser's own.

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
)

// webInlineChip is how an inline box paints around its text, in layout
// pixels: the padding and border over and under the glyphs, the corner
// radius, each border edge (left, right, top, bottom) and their colour, and
// whether it is an inline-block that makes its line taller. The words of a
// run share their run's chip.
type webInlineChip struct {
	padT, padB  int32
	radius      int32
	border      [4]int32
	borderColor sdl.Color
	block       bool
}

// hasBorder reports a chip with at least one visible border edge.
func (c *webInlineChip) hasBorder() bool {
	return c.border != [4]int32{}
}

// webInlineBorderSide is the width of one visible border edge of an inline
// box in CSS px (0 = left, 1 = right, 2 = top, 3 = bottom).
func webInlineBorderSide(st *webStyle, side int) float64 {
	if st.BorderStyle == webBorderNone || st.BorderStyle == webBorderHidden {
		return 0
	}
	visible := st.BoxBorder
	switch side {
	case 0:
		visible = visible || st.BorderLeft
	case 1:
		visible = visible || st.BorderRight
	case 2:
		visible = visible || st.BorderTop
	case 3:
		visible = visible || st.BorderBottom
	}
	if !visible {
		return 0
	}
	if w := st.BorderSideWidth[side]; w > 0 {
		return w
	}
	if st.BorderWidth > 0 {
		return st.BorderWidth
	}
	// `border: solid` with no width is `medium`.
	return 3
}

// webInlineOwnChip reports an inline element that paints a surface of its own:
// a background, or a border.
func webInlineOwnChip(st *webStyle) bool {
	if st.HasBg || st.HasGradient {
		return true
	}
	for side := 0; side < 4; side++ {
		if webInlineBorderSide(st, side) > 0 {
			return true
		}
	}
	return false
}

// webInlineEdge is the room one edge of an inline box takes in the line, in
// CSS px (margin, border and padding), and the part of it its background chip
// covers: the border and padding of a box with a surface of its own, all of
// it inside a parent's chip (inChip), none otherwise.
func webInlineEdge(st *webStyle, ownChip, inChip, left bool) (room, chip float64) {
	indent, pad, side := st.IndentRight, st.PadRight, 1
	if left {
		indent, pad, side = st.Indent, st.PadLeft, 0
	}
	pad = math.Max(0, pad)
	margin := math.Max(0, indent-pad)
	border := webInlineBorderSide(st, side)
	room = margin + pad + border
	switch {
	case ownChip:
		chip = pad + border
	case inChip:
		chip = room
	}
	return room, chip
}

// webInheritInlineChip puts an inline element, or generated content, with no
// surface of its own inside the chip of the inline box around it: it paints
// with that box's background, vertical padding, radius and border, so the
// chip runs on under it unbroken. Its own horizontal edges are measured from
// its own style before this (webInlineEdge).
func webInheritInlineChip(st, box *webStyle) {
	if !st.HasBg && box.HasBg {
		st.HasBg, st.Background = true, box.Background
	}
	st.PadTop, st.PadBot, st.Radius = box.PadTop, box.PadBot, box.Radius
	st.BoxBorder, st.BorderTop, st.BorderBottom = box.BoxBorder, box.BorderTop, box.BorderBottom
	st.BorderLeft, st.BorderRight = box.BorderLeft, box.BorderRight
	st.BorderWidth, st.BorderSideWidth, st.BorderStyle = box.BorderWidth, box.BorderSideWidth, box.BorderStyle
	st.BorderColor, st.HasBorderColor = box.BorderColor, box.HasBorderColor
	st.DisplayInlineBlock = box.DisplayInlineBlock
	st.InlineChip = true
}

// openInlineBox notes the left edge of an inline element about to open: room
// is what it takes before the first word that follows, chip the part of it
// the chip covers (webInlineEdge). The chip of the outermost box starts where
// its own chip room begins. started reports that this box is the one whose
// left edge the next word opens with (webFrame.startedChip).
func (p *webParser) openInlineBox(room, chip float64, ownChip bool) (started bool) {
	if chip > 0 && !p.pendingChip {
		p.pendingChip = true
		p.pendingChipStart = p.pendingLead + room - chip
	}
	if ownChip && !p.pendingOpens {
		p.pendingOpens, started = true, true
	}
	p.pendingLead += room
	return started
}

// takeInlineLead hands the pending left edges to the run about to be added:
// the room before its first word, the part of it the chip covers, and whether
// a box with a surface of its own opens there.
func (p *webParser) takeInlineLead() (lead, chip float64, opens bool) {
	lead, opens = p.pendingLead, p.pendingOpens
	if p.pendingChip {
		chip = math.Max(0, lead-p.pendingChipStart)
	}
	p.clearInlineLead()
	return lead, chip, opens
}

// clearInlineLead drops the pending left edges: a block boundary ends the line
// they were waiting for.
func (p *webParser) clearInlineLead() {
	p.pendingLead, p.pendingChipStart = 0, 0
	p.pendingChip, p.pendingOpens = false, false
}

// closeInlineBox puts the right edge of an inline element that just closed
// after the last run it added. An element with no text of its own (an icon
// whose glyph was dropped, an empty spacer) still takes its room before the
// text that follows, but paints no chip.
func (p *webParser) closeInlineBox(f *webFrame) {
	room, chip := f.inlineTrail, f.inlineTrailChip
	if p.current != nil && p.lineFlushes == f.inlineFlushes && len(p.current.Runs) > f.inlineRuns {
		last := &p.current.Runs[len(p.current.Runs)-1]
		last.Trail += room
		last.ChipTrail += chip
		if f.ownChip {
			last.ClosesChip = true
		}
		return
	}
	if f.startedChip && p.pendingOpens {
		// Its own chip has nothing to go round.
		p.pendingOpens, p.pendingChip, p.pendingChipStart = false, false, 0
	}
	p.pendingLead += room
}

// webButtonBox gives a submit button the box a browser draws it in: an
// unstyled one the UA padding (1px 6px) and rounded corners, a styled one its
// own padding, border and radius. It is an inline-block with a surface.
func webButtonBox(st *webStyle, authorBg bool) {
	if !authorBg && st.PadLeft <= 0 && st.PadRight <= 0 {
		st.PadLeft, st.PadRight = 6, 6
		st.Indent, st.IndentRight = st.Indent+6, st.IndentRight+6
		st.PadTop, st.PadBot = math.Max(st.PadTop, 1), math.Max(st.PadBot, 1)
		if st.Radius <= 0 {
			st.Radius = 3
		}
	}
	st.DisplayInlineBlock = true
	st.InlineChip = true
}

// webRunChip is the chip a run's inline box paints with, in layout pixels.
// The border edges are the box's own; their colour defaults to the text's.
func webRunChip(st *webStyle, baseSize float64, doc *webDocument) webInlineChip {
	scale := baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	px := func(css float64) int32 { return int32(math.Round(math.Max(0, css) * scale)) }
	c := webInlineChip{radius: px(st.Radius), block: st.DisplayInlineBlock}
	for side := 0; side < 4; side++ {
		if w := webInlineBorderSide(st, side); w > 0 {
			c.border[side] = max(px(w), 1)
		}
	}
	c.padT = px(st.PadTop) + c.border[2]
	c.padB = px(st.PadBot) + c.border[3]
	if c.hasBorder() {
		c.borderColor = st.BorderColor
		if !st.HasBorderColor {
			c.borderColor = webCurrentColor(*st, doc)
		}
	}
	return c
}

// webInlineInsets is the width a run's own inline edges add to its line, in
// layout pixels: what an intrinsic measure of the line has to count.
func webInlineInsets(run *webRun, baseSize float64) int32 {
	return int32(math.Round((run.Lead + run.Trail) * baseSize / 16))
}

// inlineBlockSpan measures the inline-block box that opens at runs[0], in
// layout pixels: its edges and the words of every run up to the one that
// closes it, one space apart. n is how many runs it spans; 0 when its end is
// not among the next few runs (a box that is not short enough to keep whole).
func (l *webLayouter) inlineBlockSpan(runs []webRun) (width int32, n int) {
	scale := l.baseSize / 16
	if scale <= 0 {
		scale = 1
	}
	for i := range runs {
		if i >= 16 {
			break
		}
		run := &runs[i]
		if run.Image != nil {
			return 0, 0
		}
		font := l.styledFont(run.Style)
		if font == nil {
			return 0, 0
		}
		text := webTransformWord(strings.Join(strings.Fields(run.Text), " "), run.Style.Transform)
		if text != "" {
			if strings.HasPrefix(run.Text, " ") && i > 0 {
				text = " " + text
			}
			w, _, err := font.SizeUTF8(text)
			if err != nil {
				return 0, 0
			}
			width += int32(w)
			if ls := int32(math.Round(run.Style.LetterSpacing * scale)); ls != 0 {
				width += ls * int32(max(utf8.RuneCountInString(text)-1, 0))
			}
		}
		width += webInlineInsets(run, l.baseSize)
		if run.ClosesChip {
			return width, i + 1
		}
	}
	return 0, 0
}

// --- Paint ---------------------------------------------------------------------

// webPaintChipSpan paints the chip of the inline box fragment that starts at
// runs[i]: the stretches after it with the same chip and background join it
// up to the box's right edge or the start of another box. A fragment the
// line broke has no side border at the break, as with box-decoration-break:
// slice, and a middle piece square corners. It returns the index of the last
// stretch it covered. Render thread.
func webPaintChipSpan(r *sdl.Renderer, runs []webPaintRun, i int, x0, y int32) int {
	first := &runs[i].run
	c := first.Chip
	last, h := i, runs[i].h
	for j := i + 1; j < len(runs); j++ {
		prev, next := &runs[last].run, &runs[j].run
		if prev.ChipRight || next.ChipLeft || next.Chip == nil || *next.Chip != *c ||
			next.HasBg != first.HasBg || next.Bg != first.Bg || next.Font == nil || next.BoxW > 0 {
			break
		}
		last, h = j, max(h, runs[j].h)
	}
	end := &runs[last]
	left := x0 + first.X - first.ChipLead
	right := x0 + end.run.X + end.w + end.run.ChipTrail
	if next := last + 1; !end.run.ChipRight && next < len(runs) && runs[next].run.Chip != nil {
		// A box nested in this one (a badge inside a button) paints its own
		// chip next; this one still runs on up to it.
		right = max(right, x0+runs[next].run.X-runs[next].run.ChipLead)
	}
	top := y + first.VerticalOffset - c.padT
	w, boxH := right-left, h+c.padT+c.padB
	if w <= 0 || boxH <= 0 {
		return last
	}
	whole := first.ChipLeft && end.run.ChipRight
	radius := c.radius
	if !first.ChipLeft && !end.run.ChipRight {
		// A middle piece of a box broken over three lines or more.
		radius = 0
	}
	if first.HasBg {
		webPaintFill(r, left, top, w, boxH, radius, first.Bg)
	}
	if !c.hasBorder() {
		return last
	}
	b := c.border
	if whole && b[0] == b[1] && b[1] == b[2] && b[2] == b[3] {
		webPaintStroke(r, left, top, w, boxH, radius, b[0], c.borderColor)
		return last
	}
	_ = r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	_ = r.SetDrawColor(c.borderColor.R, c.borderColor.G, c.borderColor.B, c.borderColor.A)
	var sides [4]sdl.Rect
	n := 0
	if b[2] > 0 {
		sides[n] = sdl.Rect{X: left, Y: top, W: w, H: b[2]}
		n++
	}
	if b[3] > 0 {
		sides[n] = sdl.Rect{X: left, Y: top + boxH - b[3], W: w, H: b[3]}
		n++
	}
	if b[0] > 0 && first.ChipLeft {
		sides[n] = sdl.Rect{X: left, Y: top, W: b[0], H: boxH}
		n++
	}
	if b[1] > 0 && end.run.ChipRight {
		sides[n] = sdl.Rect{X: right - b[1], Y: top, W: b[1], H: boxH}
		n++
	}
	if n > 0 {
		_ = r.FillRects(sides[:n])
	}
	return last
}
