package main

// webpage_headless_features_test.go widens the real draw-path matrix beyond
// the original structural fixtures. Each test drives HTML and CSS through
// parseWebPage -> fetchWebImages -> browserPageLayout -> drawWebDocument and
// asserts geometry and painted pixels, not merely that layout returned.
//
// Known gaps intentionally exercised as safe fallbacks (the full contract is
// in CSS_SUPPORT.md):
//   - transform/transition/animation/filter/clip-path are accepted but paint
//     in normal flow, with no visual transform (Test...TransformsStayInFlow).
//   - position:fixed is painted as an absolutely positioned layer anchored to
//     the viewport-sized page content box, because the reader has no live
//     scroll viewport.
//   - list-style-image and list-style-position are text markers only.
//   - @font-face, @keyframes and @page do not add rendered features; @import is
//     fetched from linked and inline stylesheets (webpage_css_import_test.go).

import (
	"fmt"
	"image"
	"image/color"
	"sort"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func headlessRuns(layout *webLayout, text string) []webRunPos {
	var runs []webRunPos
	if layout == nil {
		return runs
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text == text {
				runs = append(runs, run)
			}
		}
	}
	return runs
}

type headlessPlacedRun struct {
	webRunPos
	Y int32
}

func headlessPlacedRuns(layout *webLayout, text string) []headlessPlacedRun {
	var runs []headlessPlacedRun
	if layout == nil {
		return runs
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text == text {
				runs = append(runs, headlessPlacedRun{webRunPos: run, Y: row.Y})
			}
		}
	}
	return runs
}

type headlessRowSpan struct {
	Y     int32
	Left  int32
	Right int32
	Text  []string
}

func headlessRowsInBox(layout *webLayout, box sdl.Rect) []headlessRowSpan {
	byY := map[int32]*headlessRowSpan{}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Font == nil || run.Text == "" {
				continue
			}
			if run.X < box.X || run.X >= box.X+box.W || row.Y < box.Y || row.Y >= box.Y+box.H {
				continue
			}
			span := byY[row.Y]
			if span == nil {
				span = &headlessRowSpan{Y: row.Y, Left: run.X}
				byY[row.Y] = span
			}
			w, _, err := run.Font.SizeUTF8(run.Text)
			if err != nil {
				continue
			}
			if run.X < span.Left {
				span.Left = run.X
			}
			if right := run.X + int32(w); right > span.Right {
				span.Right = right
			}
			span.Text = append(span.Text, run.Text)
		}
	}
	rows := make([]headlessRowSpan, 0, len(byY))
	for _, span := range byY {
		rows = append(rows, *span)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Y < rows[j].Y })
	return rows
}

func headlessInkRight(img *image.NRGBA, r sdl.Rect) (int32, bool) {
	right, found := int32(-1), false
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			c := headlessPixelAt(img, x, y)
			if c.A > 128 && c.R < 150 && c.G < 150 && c.B < 150 {
				if !found || x > right {
					right, found = x, true
				}
			}
		}
	}
	return right, found
}

func headlessInkLeft(img *image.NRGBA, r sdl.Rect) (int32, bool) {
	left, found := int32(1<<30-1), false
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			c := headlessPixelAt(img, x, y)
			if c.A > 128 && c.R < 150 && c.G < 150 && c.B < 150 {
				if !found || x < left {
					left, found = x, true
				}
			}
		}
	}
	return left, found
}

func headlessHasTextContaining(layout *webLayout, part string) bool {
	if layout == nil {
		return false
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if strings.Contains(run.Text, part) {
				return true
			}
		}
	}
	return false
}

func headlessBackgroundAround(layout *webLayout, text string) (webBackgroundPaint, bool) {
	r, ok := headlessTextRect(layout, text)
	if !ok {
		return webBackgroundPaint{}, false
	}
	best := webBackgroundPaint{}
	bestArea := int32(1<<30 - 1)
	for _, bg := range layout.Backgrounds {
		if bg.Rect.W <= 0 || bg.Rect.H <= 0 {
			continue
		}
		if r.X < bg.Rect.X || r.Y < bg.Rect.Y ||
			r.X+r.W > bg.Rect.X+bg.Rect.W || r.Y+r.H > bg.Rect.Y+bg.Rect.H {
			continue
		}
		if area := bg.Rect.W * bg.Rect.H; area < bestArea {
			best, bestArea = bg, area
		}
	}
	return best, bestArea != int32(1<<30-1)
}

func headlessImageBackgroundAround(layout *webLayout, text string) (webBackgroundPaint, bool) {
	r, ok := headlessTextRect(layout, text)
	if !ok {
		return webBackgroundPaint{}, false
	}
	best := webBackgroundPaint{}
	bestArea := int32(1<<30 - 1)
	for _, bg := range layout.Backgrounds {
		if bg.Image == "" || bg.Rect.W <= 0 || bg.Rect.H <= 0 {
			continue
		}
		if r.X < bg.Rect.X || r.Y < bg.Rect.Y ||
			r.X+r.W > bg.Rect.X+bg.Rect.W || r.Y+r.H > bg.Rect.Y+bg.Rect.H {
			continue
		}
		if area := bg.Rect.W * bg.Rect.H; area < bestArea {
			best, bestArea = bg, area
		}
	}
	return best, bestArea != int32(1<<30-1)
}

func headlessRunPrefixRect(layout *webLayout, prefix string) (sdl.Rect, bool) {
	if layout == nil {
		return sdl.Rect{}, false
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Font == nil || !strings.HasPrefix(strings.Join(strings.Fields(run.Text), " "), prefix) {
				continue
			}
			w, h, err := run.Font.SizeUTF8(run.Text)
			if err != nil {
				continue
			}
			return sdl.Rect{X: run.X, Y: row.Y, W: int32(w), H: int32(h)}, true
		}
	}
	return sdl.Rect{}, false
}

func (h *headlessBrowser) textRectPrefix(layout *webLayout, prefix string) sdl.Rect {
	h.t.Helper()
	r, ok := headlessRunPrefixRect(layout, prefix)
	if !ok {
		h.t.Fatalf("layout has no text run starting with %q; runs=%v", prefix, headlessRunTexts(layout))
	}
	return r
}

func headlessStrokeAround(layout *webLayout, text string) (webRect, bool) {
	r, ok := headlessTextRect(layout, text)
	if !ok {
		return webRect{}, false
	}
	best := webRect{}
	bestArea := int32(1<<30 - 1)
	for _, rect := range layout.Rects {
		if rect.Rect.W <= 0 || rect.Rect.H <= 0 || !(rect.Stroke || rect.Radius > 0) {
			continue
		}
		if r.X < rect.Rect.X || r.Y < rect.Rect.Y ||
			r.X+r.W > rect.Rect.X+rect.Rect.W || r.Y+r.H > rect.Rect.Y+rect.Rect.H {
			continue
		}
		if area := rect.Rect.W * rect.Rect.H; area < bestArea {
			best, bestArea = rect, area
		}
	}
	return best, bestArea != int32(1<<30-1)
}

func headlessControlBox(t *testing.T, layout *webLayout, match func(*webControl) bool) (*webControl, sdl.Rect) {
	t.Helper()
	for _, hit := range layout.Links {
		if hit.Control == nil || !match(hit.Control) {
			continue
		}
		if hit.Box.W <= 0 || hit.Box.H <= 0 {
			t.Fatalf("control %q has a collapsed hit box: %+v", hit.Control.Kind, hit.Box)
		}
		return hit.Control, hit.Box
	}
	kinds := make([]string, 0, len(layout.Links))
	for _, hit := range layout.Links {
		if hit.Control != nil {
			kinds = append(kinds, hit.Control.Kind+":"+hit.Control.Display())
		}
	}
	t.Fatalf("no matching form control; controls=%v", kinds)
	return nil, sdl.Rect{}
}

func headlessCountNear(img *image.NRGBA, r sdl.Rect, want sdl.Color, tolerance int) (matched, total int) {
	return countPixels(img, r, func(c color.NRGBA) bool {
		return headlessNear(c, want, tolerance)
	})
}

func headlessRectOverlapsAny(r sdl.Rect, boxes []sdl.Rect) bool {
	for _, other := range boxes {
		if headlessOverlap(r, other, 2) {
			return true
		}
	}
	return false
}

func TestBrowserHeadlessInlineFormattingAndWhitespace(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:18px;line-height:1.4}
		p{width:420px;margin:0 0 14px}
		a{color:#0645ad;text-decoration:underline}
		code{background:#eef;color:#123}
		pre{margin:0;background:#f5f5f5;padding:8px}
	</style>
	<p>The <strong>bold</strong> and <em>italic</em> words sit beside
	<code>inline code</code> and a <a href="#target">linked phrase</a>.
	This deliberately long sentence has to wrap at the narrow measure
	without colliding with the following line.</p>
	<pre>alpha  beta
gamma line</pre>`
	_, layout := h.load("inline-formatting", html)
	raster := h.rasterize("inline-formatting")

	bold := h.textRect(layout, "bold")
	italic := h.textRect(layout, "italic")
	code := h.textRect(layout, "inline")
	link := h.textRect(layout, "linked")
	wrap := h.textRect(layout, "colliding")
	for label, r := range map[string]sdl.Rect{"bold": bold, "italic": italic, "code": code, "link": link, "wrapped line": wrap} {
		h.requireInside("inline "+label, r)
	}
	if !(bold.Y == italic.Y && italic.Y == code.Y) {
		t.Fatalf("inline elements did not share one line: bold=%+v italic=%+v code=%+v", bold, italic, code)
	}
	if !(bold.X < italic.X && italic.X < code.X) {
		t.Fatalf("inline elements are out of source order: bold=%+v italic=%+v code=%+v", bold, italic, code)
	}
	if link.Y < italic.Y {
		t.Fatalf("wrapped link ran above its source line: italic=%+v link=%+v", italic, link)
	}
	if wrap.Y <= bold.Y {
		t.Fatalf("long paragraph did not wrap: first=%+v wrapped=%+v", bold, wrap)
	}
	if runs := headlessRuns(layout, "bold"); len(runs) == 0 || !runs[0].Bold {
		t.Fatalf("strong text did not carry bold: %+v", runs)
	}
	if runs := headlessRuns(layout, "italic"); len(runs) == 0 || runs[0].Font == nil ||
		runs[0].Font.GetStyle()&ttf.STYLE_ITALIC == 0 {
		t.Fatalf("em text did not select an italic face: %+v", runs)
	}
	if runs := headlessRuns(layout, "linked"); len(runs) == 0 || !runs[0].Underline || runs[0].Href == "" {
		t.Fatalf("anchor did not carry underline/link: %+v", runs)
	}
	if !headlessHasTextContaining(layout, "alpha  beta") {
		t.Fatalf("preformatted double space was lost: runs=%v", headlessRunTexts(layout))
	}
	pre := h.textRect(layout, "gamma line")
	if pre.Y <= bold.Y {
		t.Fatalf("pre line did not preserve its line break: %+v", pre)
	}
	for _, text := range []string{"bold", "italic", "linked", "colliding", "gamma line"} {
		h.requireTextPixels(raster, layout, text, "inline formatting")
	}
	h.requireClearRightGutter(raster, "inline formatting")
}

func TestBrowserHeadlessWhiteSpaceModes(t *testing.T) {
	h := newHeadlessBrowser(t)
	nowrapText := "Nowrap keeps this entire deliberately long sentence on exactly one line even though it is much wider than its container"
	html := `<style>
		body{margin:0;font-size:18px;line-height:1.45}
		.box{width:360px;margin:0 0 14px;padding:10px;background:#eef;border:1px solid #99a}
		.normal{white-space:normal}
		.nowrap{white-space:nowrap}
		.prewrap{white-space:pre-wrap}
		.pre{white-space:pre}
		.inlinebox{width:320px;margin:0 0 14px}
		.inline-nowrap{white-space:nowrap}
	</style>
	<div class="box normal">Normal mode collapses    repeated spaces and wraps this sentence across more than one line in the narrow box.</div>
	<div class="box nowrap">` + nowrapText + `</div>
	<div class="box prewrap">Pre wrap begins
    indented   words

After the blank line</div>
	<div class="box pre">Pre begins
    fixed   code

After the blank line</div>
	<p class="inlinebox">Before <span class="inline-nowrap">inline nowrap span keeps this phrase on one line even though it is far wider than the paragraph allows</span> after.</p>`
	_, layout := h.load("white-space-modes", html)
	raster := h.rasterize("white-space-modes")

	normal := h.textRect(layout, "Normal")
	normalTail := h.textRect(layout, "narrow")
	if normalTail.Y <= normal.Y {
		t.Fatalf("normal white-space stopped wrapping: first=%+v tail=%+v", normal, normalTail)
	}
	normalBox, ok := headlessBackgroundAround(layout, "Normal")
	if !ok {
		t.Fatal("normal box background missing")
	}
	normalRows := headlessRowsInBox(layout, normalBox.Rect)
	if len(normalRows) == 0 {
		t.Fatal("normal white-space produced no runs")
	}
	for _, token := range normalRows[0].Text {
		if strings.Contains(token, "  ") {
			t.Fatalf("normal white-space stopped collapsing spaces: %q", token)
		}
	}

	nowrapBox, ok := headlessBackgroundAround(layout, "Nowrap")
	if !ok {
		t.Fatal("nowrap box background missing")
	}
	nowrapRows := headlessRowsInBox(layout, nowrapBox.Rect)
	if len(nowrapRows) != 1 {
		t.Fatalf("nowrap wrapped onto %d rows: %+v", len(nowrapRows), nowrapRows)
	}
	nowrapRun := headlessRuns(layout, "Nowrap")
	if len(nowrapRun) == 0 || nowrapRun[0].Font == nil {
		t.Fatalf("nowrap text missing: %v", headlessRunTexts(layout))
	}
	fullW, _, err := nowrapRun[0].Font.SizeUTF8(nowrapText)
	if err != nil {
		t.Fatalf("measure nowrap text: %v", err)
	}
	if int32(fullW) <= nowrapBox.Rect.W-20 {
		t.Fatalf("nowrap fixture is not wider than its box: text=%d box=%+v", fullW, nowrapBox.Rect)
	}
	if nowrapRows[0].Right > nowrapBox.Rect.X+nowrapBox.Rect.W {
		t.Fatalf("nowrap text painted past its box: row=%+v box=%+v", nowrapRows[0], nowrapBox.Rect)
	}
	if !headlessHasTextContaining(layout, "…") {
		t.Fatalf("nowrap overflow was not ellipsized: runs=%v", headlessRunTexts(layout))
	}
	nowrapBand := sdl.Rect{X: nowrapBox.Rect.X + 7, Y: nowrapRows[0].Y, W: nowrapBox.Rect.W, H: 28}
	if _, ok := headlessInkRight(raster, nowrapBand); !ok {
		t.Fatalf("nowrap line painted no readable pixels in %+v", nowrapBand)
	}

	preWrapIndented := headlessPlacedRuns(layout, "    indented")
	if len(preWrapIndented) == 0 {
		t.Fatalf("pre-wrap lost its leading spaces: runs=%v", headlessRunTexts(layout))
	}
	preWrapWords := headlessPlacedRuns(layout, "   words")
	if len(preWrapWords) == 0 {
		t.Fatalf("pre-wrap lost its repeated spaces: runs=%v", headlessRunTexts(layout))
	}
	preWrapBegin := headlessPlacedRuns(layout, "Pre")
	if len(preWrapBegin) == 0 || preWrapBegin[0].Y >= preWrapIndented[0].Y {
		t.Fatalf("pre-wrap lost its authored line break: begin=%+v indented=%+v", preWrapBegin, preWrapIndented[0])
	}
	if preWrapWords[0].Y != preWrapIndented[0].Y {
		t.Fatalf("pre-wrap split one authored line: indented=%+v words=%+v", preWrapIndented[0], preWrapWords[0])
	}
	preWrapAfter := headlessPlacedRuns(layout, "After")
	preAfter := headlessPlacedRuns(layout, "After the blank line")
	if len(preWrapAfter) == 0 || len(preAfter) == 0 {
		t.Fatalf("expected blank lines in both preserved blocks: runs=%v", headlessRunTexts(layout))
	}
	if preWrapAfter[0].Y-preWrapWords[0].Y < 2*26 {
		t.Fatalf("pre-wrap blank line was collapsed: words=%+v after=%+v", preWrapWords[0], preWrapAfter[0])
	}
	spaceW, _, err := preWrapIndented[0].Font.SizeUTF8(" ")
	if err != nil {
		t.Fatalf("measure space: %v", err)
	}
	indentW, _, err := preWrapIndented[0].Font.SizeUTF8("    ")
	if err != nil {
		t.Fatalf("measure indent: %v", err)
	}
	runRasterX := 7 + preWrapIndented[0].X
	inkBox := sdl.Rect{X: runRasterX - 2, Y: preWrapIndented[0].Y, W: int32(indentW) + 40, H: 28}
	inkLeft, ok := headlessInkLeft(raster, inkBox)
	if !ok {
		t.Fatalf("pre-wrap indented line painted no ink in %+v", inkBox)
	}
	if inkLeft < runRasterX+int32(spaceW)*3 {
		t.Fatalf("pre-wrap leading spaces were not visible: ink starts at %d, run at %d, space=%d", inkLeft, runRasterX, spaceW)
	}

	fixed := headlessPlacedRuns(layout, "    fixed   code")
	if len(fixed) == 0 {
		t.Fatalf("pre lost its spaces or merged its line: runs=%v", headlessRunTexts(layout))
	}
	if fixed[0].Y <= preWrapIndented[0].Y {
		t.Fatalf("pre block did not follow the pre-wrap block: fixed=%+v prewrap=%+v", fixed[0], preWrapIndented[0])
	}
	if preAfter[0].Y-fixed[0].Y < 2*26 {
		t.Fatalf("pre blank line was collapsed: fixed=%+v after=%+v", fixed[0], preAfter[0])
	}

	before := h.textRect(layout, "Before")
	inline := h.textRect(layout, "inline")
	after := h.textRect(layout, "after.")
	if inline.Y != before.Y || after.Y-inline.Y > 40 {
		t.Fatalf("inline nowrap span wrapped instead of clipping: before=%+v inline=%+v after=%+v", before, inline, after)
	}
	for _, text := range []string{"Normal", "Nowrap", "    indented", "    fixed   code", "Before", "inline", "after."} {
		h.requireTextPixels(raster, layout, text, "white-space mode")
	}
	h.requireClearRightGutter(raster, "white-space modes")
}

func TestBrowserHeadlessJustifiedText(t *testing.T) {
	h := newHeadlessBrowser(t)
	body := strings.Repeat("Justified lines stretch every word gap until the line reaches the right content edge while the final line stays natural. ", 4)
	html := `<style>
		body{margin:0;font-size:20px;line-height:1.5}
		p{margin:0 0 18px}
		.just{width:460px;text-align:justify;background:#eef}
		.center{width:460px;text-align:center;background:#efe}
		.right{width:460px;text-align:right;background:#fee}
	</style>
	<p class="just">` + body + `</p>
	<p class="center">Center aligned reference</p>
	<p class="right">Right aligned reference</p>`
	_, layout := h.load("justified-text", html)
	raster := h.rasterize("justified-text")

	box, ok := headlessBackgroundAround(layout, "Justified")
	if !ok {
		t.Fatal("justified block background missing")
	}
	rows := headlessRowsInBox(layout, box.Rect)
	if len(rows) < 3 {
		t.Fatalf("justified fixture produced only %d lines: %+v", len(rows), rows)
	}
	contentRight := box.Rect.X + box.Rect.W - 4
	for i, row := range rows[:len(rows)-1] {
		if row.Right < contentRight-6 {
			t.Fatalf("justified line %d did not reach the content edge: right=%d edge=%d row=%+v", i, row.Right, contentRight, row)
		}
		band := sdl.Rect{X: box.Rect.X + 7, Y: row.Y, W: box.Rect.W, H: 30}
		inkRight, ok := headlessInkRight(raster, band)
		if !ok {
			t.Fatalf("justified line %d painted no ink in %+v", i, band)
		}
		if delta := inkRight - (contentRight + 7); delta < -8 || delta > 8 {
			t.Fatalf("justified line %d painted text to %d, content edge is %d", i, inkRight, contentRight+7)
		}
	}
	last := rows[len(rows)-1]
	if last.Right >= contentRight-12 {
		t.Fatalf("last justified line was stretched to the edge: %+v", last)
	}
	lastBand := sdl.Rect{X: box.Rect.X + 7, Y: last.Y, W: box.Rect.W, H: 30}
	if inkRight, ok := headlessInkRight(raster, lastBand); ok && inkRight >= contentRight+7-6 {
		t.Fatalf("last justified line painted to the content edge: ink=%d edge=%d", inkRight, contentRight+7)
	}

	centerBox, ok := headlessBackgroundAround(layout, "Center")
	if !ok {
		t.Fatal("centered box background missing")
	}
	centerRows := headlessRowsInBox(layout, centerBox.Rect)
	if len(centerRows) != 1 || centerRows[0].Left <= centerBox.Rect.X+8 || centerRows[0].Right >= centerBox.Rect.X+centerBox.Rect.W-8 {
		t.Fatalf("centered reference line is not centered: rows=%+v box=%+v", centerRows, centerBox.Rect)
	}
	rightBox, ok := headlessBackgroundAround(layout, "Right")
	if !ok {
		t.Fatal("right-aligned box background missing")
	}
	rightRows := headlessRowsInBox(layout, rightBox.Rect)
	if len(rightRows) != 1 || rightRows[0].Right < rightBox.Rect.X+rightBox.Rect.W-10 {
		t.Fatalf("right-aligned reference line stopped short of the edge: rows=%+v box=%+v", rightRows, rightBox.Rect)
	}
	h.requireTextPixels(raster, layout, "Justified", "justified text")
	h.requireTextPixels(raster, layout, "Center", "centered reference")
	h.requireTextPixels(raster, layout, "Right", "right reference")
	h.requireClearRightGutter(raster, "justified text")
}

func TestBrowserHeadlessNestedLists(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:18px}
		ul,ol{margin:0 0 12px;padding-left:28px}
		li{margin:6px 0}
	</style>
	<ul>
		<li>Outer one
			<ul><li>Nested alpha</li><li>Nested beta</li></ul>
		</li>
		<li>Outer two</li>
	</ul>
	<ol><li>First numbered</li><li>Second numbered</li></ol>`
	_, layout := h.load("nested-lists", html)
	raster := h.rasterize("nested-lists")

	outer := h.textRect(layout, "Outer")
	innerAlpha := h.textRect(layout, "Nested")
	first := h.textRect(layout, "First")
	second := h.textRect(layout, "Second")
	if !(outer.Y < innerAlpha.Y && innerAlpha.Y < first.Y) {
		t.Fatalf("list blocks did not flow top to bottom: outer=%+v inner=%+v first=%+v", outer, innerAlpha, first)
	}
	if innerAlpha.X <= outer.X {
		t.Fatalf("nested list is not indented past its parent: outer=%+v nested=%+v", outer, innerAlpha)
	}
	if second.Y <= first.Y {
		t.Fatalf("numbered list items share a line: first=%+v second=%+v", first, second)
	}
	if !headlessHasTextContaining(layout, "•") {
		t.Fatalf("unordered list marker is missing: runs=%v", headlessRunTexts(layout))
	}
	if !headlessHasTextContaining(layout, "1.") {
		t.Fatalf("ordered list marker is missing: runs=%v", headlessRunTexts(layout))
	}
	for _, text := range []string{"Outer", "Nested", "First", "Second"} {
		h.requireTextPixels(raster, layout, text, "nested list")
	}
	h.requireClearRightGutter(raster, "nested lists")
}

func TestBrowserHeadlessFloatsAndClear(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:17px}
		.float{float:left;width:180px;min-height:130px;background:#9cf;padding:10px}
		.beside{margin:0}
		.after{clear:both;background:#fee;padding:10px}
	</style>
	<div class="float">Float box</div>
	<p class="beside">Words beside the float continue along the narrowed
	measure and wrap onto another line before the float ends.</p>
	<p class="after">After clear</p>`
	_, layout := h.load("floats-clear", html)
	raster := h.rasterize("floats-clear")

	floatRun, index := h.floatTextRect(layout, "Float")
	if index < 0 || index >= len(layout.Floats) {
		t.Fatal("float text has no painted layer")
	}
	floatRect := h.floatBoxAround(layout, "Float")
	beside := h.textRect(layout, "beside")
	after := h.textRect(layout, "After")
	if beside.X <= floatRect.X {
		t.Fatalf("text beside the float did not start to its right: float=%+v beside=%+v", floatRect, beside)
	}
	if after.Y < floatRect.Y+floatRect.H-2 {
		t.Fatalf("clear did not move the following block below the float: float=%+v after=%+v", floatRect, after)
	}
	if !headlessHasTextContaining(layout, "measure") {
		t.Fatalf("beside text lost its wrapping content: runs=%v", headlessRunTexts(layout))
	}
	floatBox := h.floatBoxAround(layout, "Float")
	h.requireFill(raster, floatBox, sdl.Color{R: 153, G: 204, B: 255, A: 255}, 0.25, "float surface")
	h.requireInk(raster, floatRun, "float layer label")
	h.requireTextPixels(raster, layout, "beside", "text beside float")
	h.requireTextPixels(raster, layout, "After", "clear paragraph")
	h.requireClearRightGutter(raster, "floats")
}

func TestBrowserHeadlessBordersAndRadius(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:18px}
		.panel{width:360px;margin:12px 0;padding:14px;border:4px solid #c33;background:#fff}
		.pill{width:220px;margin:12px 0;padding:10px;border:3px solid #36c;
			border-radius:999px;background:#def}
	</style>
	<div class="panel">Bordered panel</div>
	<div class="pill">Rounded badge</div>`
	_, layout := h.load("borders-radius", html)
	raster := h.rasterize("borders-radius")

	pillBox := h.boxAround(layout, "Rounded")
	panelBorder, ok := headlessStrokeAround(layout, "Bordered")
	if !ok || !panelBorder.Stroke {
		t.Fatalf("bordered panel has no stroke rect: %+v", panelBorder)
	}
	pillBorder, ok := headlessStrokeAround(layout, "Rounded")
	if !ok || pillBorder.Radius <= 0 {
		t.Fatalf("rounded badge has no radius: %+v", pillBorder)
	}
	if pillBorder.Radius > pillBox.H/2+2 {
		t.Fatalf("pill radius was not clamped to its height: radius=%d box=%+v", pillBorder.Radius, pillBox)
	}
	red := sdl.Color{R: 0xcc, G: 0x33, B: 0x33, A: 255}
	blue := sdl.Color{R: 0x33, G: 0x66, B: 0xcc, A: 255}
	panel := panelBorder.Rect
	for _, probe := range []struct {
		name string
		r    sdl.Rect
	}{
		{"left", sdl.Rect{X: panel.X - 8, Y: panel.Y + panel.H/2 - 10, W: 24, H: 20}},
		{"right", sdl.Rect{X: panel.X + panel.W - 16, Y: panel.Y + panel.H/2 - 10, W: 24, H: 20}},
		{"top", sdl.Rect{X: panel.X + panel.W/2 - 10, Y: panel.Y - 8, W: 20, H: 24}},
		{"bottom", sdl.Rect{X: panel.X + panel.W/2 - 10, Y: panel.Y + panel.H - 16, W: 20, H: 24}},
	} {
		if matched, _ := headlessCountNear(raster, probe.r, red, 20); matched < 12 {
			t.Errorf("panel %s border did not paint red: %d pixels in %+v", probe.name, matched, probe.r)
		}
	}
	pill := pillBorder.Rect
	if matched, _ := headlessCountNear(raster, sdl.Rect{X: pill.X - 8, Y: pill.Y + pill.H/2 - 10, W: 24, H: 20}, blue, 20); matched < 12 {
		t.Errorf("pill left border did not paint blue: %d pixels in %+v", matched, pill)
	}
	corner := headlessPixelAt(raster, pillBox.X+1, pillBox.Y+1)
	if !(corner.R > 245 && corner.G > 245 && corner.B > 245) {
		t.Errorf("rounded corner was not clipped: pixel=%v box=%+v", corner, pillBox)
	}
	h.requireTextPixels(raster, layout, "Bordered", "bordered panel")
	h.requireTextPixels(raster, layout, "Rounded", "rounded badge")
	h.requireClearRightGutter(raster, "borders")
}

func TestBrowserHeadlessOpacityPaintsThrough(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:18px}
		.fade,.solid{margin:10px 0;padding:14px;width:320px;background:#102030;color:#fff}
		.fade{opacity:0.4}
	</style>
	<div class="fade">Faded surface</div>
	<div class="solid">Solid surface</div>`
	_, layout := h.load("opacity", html)
	raster := h.rasterize("opacity")

	fadeBox := h.boxAround(layout, "Faded")
	solidBox := h.boxAround(layout, "Solid")
	fadeSample := sdl.Rect{X: fadeBox.X + fadeBox.W/2 - 3, Y: fadeBox.Y + 3, W: 7, H: 6}
	solidSample := sdl.Rect{X: solidBox.X + solidBox.W/2 - 3, Y: solidBox.Y + 3, W: 7, H: 6}
	fadeLum := headlessAverageLuminance(raster, fadeSample)
	solidLum := headlessAverageLuminance(raster, solidSample)
	if fadeLum <= solidLum+12 {
		t.Fatalf("opacity did not lighten the faded surface: faded=%.1f solid=%.1f", fadeLum, solidLum)
	}
	h.requireTextPixels(raster, layout, "Faded", "faded text")
	h.requireTextPixels(raster, layout, "Solid", "solid text")
	h.requireClearRightGutter(raster, "opacity")
}

// This fixture is the deliberate no-op contract for CSS 2D transforms: the
// declaration is accepted, but nothing is silently clipped or moved into an
// unreadable position. The page keeps normal flow and full text ink.
func TestBrowserHeadlessTransformsStayInFlow(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:18px}
		.moved,.still{margin:10px 0;padding:12px;width:320px;background:#eef}
		.moved{transform:translate(80px,30px) rotate(12deg);transition:all 1s}
	</style>
	<div class="moved">Transformed marker</div>
	<div class="still">Control marker</div>`
	_, layout := h.load("transforms-ignored", html)
	raster := h.rasterize("transforms-ignored")

	moved := h.textRect(layout, "Transformed")
	control := h.textRect(layout, "Control")
	if moved.X != control.X {
		t.Fatalf("ignored transform shifted text horizontally: moved=%+v control=%+v", moved, control)
	}
	if moved.Y >= control.Y {
		t.Fatalf("transformed block left normal document flow: moved=%+v control=%+v", moved, control)
	}
	if len(layout.Floats) != 0 {
		t.Fatalf("ignored transform created a detached layer: floats=%+v", layout.Floats)
	}
	h.requireTextPixels(raster, layout, "Transformed", "transform fallback")
	h.requireTextPixels(raster, layout, "Control", "transform control")
	h.requireClearRightGutter(raster, "transforms")
}

func TestBrowserHeadlessTextMetricsAndDecoration(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:18px}
		p{margin:6px 0}
		.center{text-align:center}
		.right{text-align:right}
		.deco{text-decoration:underline}
		.spaced{letter-spacing:8px;font-size:20px}
		.normal{font-size:20px}
		.lines{line-height:2.4}
	</style>
	<p class="left">Left aligned</p>
	<p class="center">Center aligned</p>
	<p class="right">Right aligned</p>
	<p class="deco">Underlined <s>struck</s></p>
	<p class="spaced">SPACED</p>
	<p class="normal">NORMAL</p>
	<p class="lines">Line one<br>Line two</p>`
	_, layout := h.load("text-metrics", html)
	raster := h.rasterize("text-metrics")

	left := h.textRect(layout, "Left")
	center := h.textRect(layout, "Center")
	right := h.textRect(layout, "Right")
	if !(left.X < center.X && center.X < right.X) {
		t.Fatalf("text-align did not move the runs: left=%+v center=%+v right=%+v", left, center, right)
	}
	if right.X+right.W < 700 {
		t.Fatalf("right-aligned text stayed near the left edge: %+v", right)
	}
	deco := headlessRuns(layout, "Underlined")
	if len(deco) == 0 || !deco[0].Underline {
		t.Fatalf("text-decoration:underline missing: %+v", deco)
	}
	struck := headlessRuns(layout, "struck")
	if len(struck) == 0 || !struck[0].Strike {
		t.Fatalf("strikethrough missing: %+v", struck)
	}
	firstLetter := h.textRect(layout, "S")
	lastLetter := h.textRect(layout, "D")
	normal := h.textRect(layout, "NORMAL")
	if lastLetter.X <= normal.X+normal.W+20 {
		t.Fatalf("letter-spacing did not widen the run: first=%+v last=%+v normal=%+v", firstLetter, lastLetter, normal)
	}
	lineOne := h.textRectPrefix(layout, "Line")
	lineTwo := h.textRectPrefix(layout, "two")
	if lineTwo.Y-lineOne.Y < 28 {
		t.Fatalf("line-height did not increase the line gap: one=%+v two=%+v", lineOne, lineTwo)
	}
	for _, text := range []string{"Left", "Center", "Right", "Underlined", "S", "two"} {
		h.requireTextPixels(raster, layout, text, "text metrics")
	}
	h.requireClearRightGutter(raster, "text metrics")
}

func TestBrowserHeadlessTableSpanPaddingAndBorders(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:17px}
		table{width:100%;border-collapse:collapse;margin:8px 0}
		caption{caption-side:top;padding:8px;font-weight:bold}
		th,td{border:2px solid #234;padding:12px;background:#f7fbff}
		th{background:#dde8f5}
	</style>
	<table>
		<caption>Regional totals</caption>
		<tr><th colspan="2">All regions</th></tr>
		<tr><td>North</td><td>120</td></tr>
		<tr><td>South</td><td>80</td></tr>
		<tr><td colspan="2">Combined footer</td></tr>
	</table>`
	_, layout := h.load("table-span", html)
	raster := h.rasterize("table-span")

	totals := h.textRect(layout, "All")
	north := h.textRect(layout, "North")
	hundred := h.textRect(layout, "120")
	south := h.textRect(layout, "South")
	footer := h.textRect(layout, "Combined")
	if !(totals.Y < north.Y && north.Y == hundred.Y && south.Y > north.Y && footer.Y > south.Y) {
		t.Fatalf("table rows are out of order: totals=%+v north=%+v hundred=%+v south=%+v footer=%+v",
			totals, north, hundred, south, footer)
	}
	if !(north.X < hundred.X) {
		t.Fatalf("table columns are not laid out left to right: north=%+v hundred=%+v", north, hundred)
	}
	allBox := h.boxAround(layout, "All")
	northBox := h.boxAround(layout, "North")
	footerBox := h.boxAround(layout, "Combined")
	if allBox.W < northBox.W*3/2 || footerBox.W < northBox.W*3/2 {
		t.Fatalf("colspan did not widen the spanning cells: all=%+v north=%+v footer=%+v", allBox, northBox, footerBox)
	}
	if north.X <= northBox.X+5 {
		t.Fatalf("cell padding did not inset the text: box=%+v run=%+v", northBox, north)
	}
	if headlessOverlap(totals, north, 2) || headlessOverlap(north, south, 2) || headlessOverlap(south, footer, 2) {
		t.Fatalf("table text boxes overlap: totals=%+v north=%+v south=%+v footer=%+v", totals, north, south, footer)
	}
	border := sdl.Color{R: 0x22, G: 0x33, B: 0x44, A: 255}
	if matched, _ := headlessCountNear(raster, sdl.Rect{
		X: allBox.X, Y: allBox.Y, W: allBox.W, H: footerBox.Y + footerBox.H - allBox.Y,
	}, border, 25); matched < 500 {
		t.Fatalf("collapsed table borders painted too little ink: %d pixels", matched)
	}
	for _, text := range []string{"Regional", "All", "North", "120", "South", "Combined"} {
		h.requireTextPixels(raster, layout, text, "table")
	}
	h.requireClearRightGutter(raster, "table")
}

func TestBrowserHeadlessImageSizingAndFit(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:16px}
		img{margin:8px 0}
		.contain{width:240px;height:80px;object-fit:contain;background:#223}
		.crop{width:240px;height:80px;object-fit:cover}
		.max{width:500px;max-width:100px}
	</style>
	<img class="natural" src="` + headlessPhoto + `">
	<img class="fixed" src="` + headlessPhoto + `" width="120" height="60">
	<img class="crop" src="` + headlessPhoto + `">
	<img class="contain" src="` + headlessPhoto + `">
	<img class="max" src="` + headlessPhoto + `">`
	_, layout := h.load("image-sizing", html)
	raster := h.rasterize("image-sizing")

	if len(layout.Images) != 5 {
		t.Fatalf("expected five image boxes, got %d: %+v", len(layout.Images), layout.Images)
	}
	// The reader lays the block images out in source order.
	natural, fixed, crop, contain, maxed := layout.Images[0].Rect, layout.Images[1].Rect, layout.Images[2].Rect, layout.Images[3].Rect, layout.Images[4].Rect
	if natural.W <= 0 || natural.H <= 0 || natural.W > 64 || natural.H > 64 {
		t.Fatalf("natural 16px image has the wrong size: %+v", natural)
	}
	// Image sizes are CSS px on the same scale as the text: Config{} draws 16px
	// text at 24 layout pixels, so a 120px image is 180 of them.
	if fixed.W != 180 || fixed.H != 90 {
		t.Fatalf("explicit image dimensions were not honoured: %+v", fixed)
	}
	if crop.W != 360 || crop.H != 120 || contain.W != 360 || contain.H != 120 {
		t.Fatalf("object-fit boxes have the wrong dimensions: crop=%+v contain=%+v", crop, contain)
	}
	if maxed.W > 150 || maxed.W <= 0 || maxed.H <= 0 {
		t.Fatalf("max-width did not cap the large image: %+v", maxed)
	}
	if layout.Images[2].ObjectFit == 0 || layout.Images[3].ObjectFit == 0 {
		t.Fatalf("object-fit did not reach the image paint state: crop=%+v contain=%+v", layout.Images[2], layout.Images[3])
	}
	for name, rect := range map[string]sdl.Rect{"natural": natural, "fixed": fixed, "crop": crop, "contain": contain, "max": maxed} {
		h.requireInside("image "+name, rect)
	}
	if matched, total := countPixels(raster, fixed, headlessSaturatedPhoto); matched*2 < total {
		t.Fatalf("explicit image did not paint its photo pixels: %d/%d", matched, total)
	}
	if matched, total := countPixels(raster, crop, headlessSaturatedPhoto); matched*2 < total {
		t.Fatalf("cover image did not fill its box: %d/%d", matched, total)
	}
	bar := sdl.Rect{X: contain.X + 2, Y: contain.Y + 2, W: 12, H: contain.H - 4}
	if matched, total := headlessCountNear(raster, bar, sdl.Color{R: 0x22, G: 0x33, B: 0x44, A: 255}, 20); matched*2 < total {
		t.Fatalf("contain did not letterbox against its background: %d/%d pixels; image=%+v backgrounds=%+v", matched, total, layout.Images[3], layout.Backgrounds)
	}
	h.requireClearRightGutter(raster, "image sizing")
}

func TestBrowserHeadlessBackgroundLayersRepeatAndPosition(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:18px}
		.tiled,.positioned,.layered{width:300px;height:100px;margin:8px 0;padding:8px}
		.tiled{background:#123;background-image:url("` + headlessPhoto + `");
			background-repeat:repeat;background-size:32px 32px}
		.positioned{background:#def;background-image:url("` + headlessSwatch + `");
			background-repeat:no-repeat;background-size:24px 24px;background-position:right bottom}
		.layered{background:#335;background-image:linear-gradient(rgba(255,0,0,0.8),rgba(0,0,255,0.8)),url("` + headlessSwatch + `");
			background-size:cover,20px 20px;background-repeat:no-repeat,repeat}
	</style>
	<div class="tiled">Tiled surface</div>
	<div class="positioned">Positioned surface</div>
	<div class="layered">Layered surface</div>`
	doc, layout := h.load("background-layers", html)
	raster := h.rasterize("background-layers")

	tiledBox := h.boxAround(layout, "Tiled")
	positionedBox := h.boxAround(layout, "Positioned")
	layeredBox := h.boxAround(layout, "Layered")
	imageLayerIn := func(box sdl.Rect) (webBackgroundPaint, bool) {
		for _, bg := range layout.Backgrounds {
			if bg.Image == "" {
				continue
			}
			if bg.Rect.X < box.X+box.W && box.X < bg.Rect.X+bg.Rect.W &&
				bg.Rect.Y < box.Y+box.H && box.Y < bg.Rect.Y+bg.Rect.H {
				return bg, true
			}
		}
		return webBackgroundPaint{}, false
	}
	if _, ok := imageLayerIn(tiledBox); !ok {
		t.Fatalf("tiled background layer is missing: box=%+v backgrounds=%d", tiledBox, len(layout.Backgrounds))
	}
	positioned, ok := imageLayerIn(positionedBox)
	if !ok || !strings.Contains(positioned.ObjectPosition, "right") {
		t.Fatalf("positioned background layer is missing: layer=%+v box=%+v", positioned, positionedBox)
	}
	if _, ok := imageLayerIn(layeredBox); !ok {
		t.Fatalf("layered background has no image layer: box=%+v", layeredBox)
	}
	orange := sdl.Color{R: 224, G: 126, B: 42, A: 255}
	if matched, total := countPixels(raster, tiledBox, headlessSaturatedPhoto); matched*8 < total {
		bg, _ := imageLayerIn(tiledBox)
		sampleX, sampleY := bg.Rect.X+bg.Rect.W/2, bg.Rect.Y+bg.Rect.H/2
		sample := headlessPixelAt(raster, sampleX, sampleY)
		covering := make([]int, 0, 8)
		for i, candidate := range layout.Backgrounds {
			if sampleX >= candidate.Rect.X && sampleX < candidate.Rect.X+candidate.Rect.W &&
				sampleY >= candidate.Rect.Y && sampleY < candidate.Rect.Y+candidate.Rect.H {
				covering = append(covering, i)
			}
		}
		coveringRects := make([]string, 0, 4)
		for _, candidate := range layout.Rects {
			if sampleX >= candidate.Rect.X && sampleX < candidate.Rect.X+candidate.Rect.W &&
				sampleY >= candidate.Rect.Y && sampleY < candidate.Rect.Y+candidate.Rect.H {
				coveringRects = append(coveringRects, fmt.Sprintf("%v fill=%t stroke=%t", candidate.Rect, candidate.BackgroundFill, candidate.Stroke))
			}
		}
		t.Fatalf("repeating background did not tile its image: %d/%d photo pixels; box=%+v layer=%+v bytes=%d sample=%v covering=%v rects=%v", matched, total, tiledBox, bg, len(doc.imageBytes(bg.Image)), sample, covering, coveringRects)
	}
	// A repeated quadrant image must reach all four colours, not just one
	// stretched tile: this catches a background that silently ignores repeat.
	for name, c := range map[string]sdl.Color{
		"red": {R: 220, G: 42, B: 42, A: 255}, "green": {R: 36, G: 190, B: 76, A: 255},
		"blue": {R: 38, G: 82, B: 220, A: 255}, "yellow": {R: 244, G: 194, B: 34, A: 255},
	} {
		if matched, _ := headlessCountNear(raster, tiledBox, c, 20); matched < 40 {
			t.Fatalf("repeating background lost its %s quadrant: %d pixels; rect=%+v", name, matched, tiledBox)
		}
	}
	leftThird := sdl.Rect{X: positionedBox.X, Y: positionedBox.Y, W: positionedBox.W / 3, H: positionedBox.H}
	rightThird := sdl.Rect{X: positionedBox.X + positionedBox.W*2/3, Y: positionedBox.Y, W: positionedBox.W / 3, H: positionedBox.H}
	leftOrange, _ := headlessCountNear(raster, leftThird, orange, 18)
	rightOrange, _ := headlessCountNear(raster, rightThird, orange, 18)
	if rightOrange <= leftOrange+20 {
		t.Fatalf("background-position did not anchor the image at the right: left=%d right=%d", leftOrange, rightOrange)
	}
	gradients := 0
	for _, bg := range layout.Backgrounds {
		if bg.HasGradient && bg.Rect.X < layeredBox.X+layeredBox.W && layeredBox.X < bg.Rect.X+bg.Rect.W &&
			bg.Rect.Y < layeredBox.Y+layeredBox.H && layeredBox.Y < bg.Rect.Y+bg.Rect.H {
			gradients++
		}
	}
	if gradients == 0 {
		t.Fatalf("layered background collapsed to one image layer: box=%+v", layeredBox)
	}
	red := sdl.Color{R: 255, G: 0, B: 0, A: 255}
	blue := sdl.Color{R: 0, G: 0, B: 255, A: 255}
	if r, _ := headlessCountNear(raster, layeredBox, red, 45); r < 200 {
		t.Fatalf("layered gradient lost its red stop: %d pixels", r)
	}
	if b, _ := headlessCountNear(raster, layeredBox, blue, 45); b < 200 {
		t.Fatalf("layered gradient lost its blue stop: %d pixels", b)
	}
	if matched, total := countPixels(raster, layeredBox, func(c color.NRGBA) bool {
		return c.R > 80 && c.B > 50 || headlessOrange(c)
	}); matched*10 < total {
		t.Fatalf("gradient/image layers painted too little: %d/%d", matched, total)
	}
	for _, text := range []string{"Tiled", "Positioned", "Layered"} {
		h.requireTextPixels(raster, layout, text, "background layers")
	}
	h.requireClearRightGutter(raster, "background layers")
}

func TestBrowserHeadlessPositionedOffsets(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:17px}
		.rel{position:relative;left:30px;top:6px;width:300px;background:#eef;padding:10px}
		.stage{position:relative;height:150px;margin-top:10px;padding:10px;background:#f7f7f7}
		.stage-label{padding:0}
		.abs{position:absolute;top:24px;right:12px;width:170px;background:#fea;padding:8px}
		.fixed{position:fixed;top:0;right:0;width:180px;background:#334;color:#fff;padding:8px}
	</style>
	<div class="rel">Relative marker</div>
	<div class="stage"><div class="stage-label">Stage label</div><div class="abs">Absolute badge</div></div>
	<p>Flow after the stage</p>
	<div class="fixed">Fixed bar</div>`
	_, layout := h.load("positioned", html)
	raster := h.rasterize("positioned")

	rel := h.textRect(layout, "Relative")
	if rel.X < 20 {
		t.Fatalf("relative left offset was ignored: %+v", rel)
	}
	stage := h.boxAround(layout, "Stage")
	absRun, absIndex := h.floatTextRect(layout, "Absolute")
	_, fixedIndex := h.floatTextRect(layout, "Fixed")
	if absIndex < 0 || fixedIndex < 0 {
		t.Fatal("positioned layers are missing")
	}
	abs := h.floatBoxAround(layout, "Absolute")
	fixed := h.floatBoxAround(layout, "Fixed")
	if abs.X <= stage.X || abs.Y <= stage.Y || abs.X+abs.W > stage.X+stage.W+2 {
		t.Fatalf("absolute badge is not anchored inside its relative stage: stage=%+v abs=%+v", stage, abs)
	}
	if absRun.Y < stage.Y+10 {
		t.Fatalf("absolute top offset was ignored: stage=%+v run=%+v", stage, absRun)
	}
	if fixed.Y > 20 || fixed.X+fixed.W < h.pageBounds().W-20 {
		t.Fatalf("fixed layer is not anchored to the page's top-right: %+v page=%+v", fixed, h.pageBounds())
	}
	if flow := h.textRect(layout, "Flow"); flow.Y <= stage.Y+stage.H-10 {
		t.Fatalf("absolute layer removed its stage from flow: stage=%+v flow=%+v", stage, flow)
	}
	h.requireTextPixels(raster, layout, "Relative", "relative marker")
	h.requireInk(raster, absRun, "absolute badge")
	fixedLabel, _ := h.floatTextRect(layout, "Fixed")
	h.requireInk(raster, fixedLabel, "fixed bar")
	h.requireClearRightGutter(raster, "positioned")
}

func TestBrowserHeadlessMediaQueriesAndViewportMeta(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>
		body{margin:0;font-size:17px}
		.probe,.desktop,.mobile,.landscape{height:60px;padding:10px;margin:6px 0;background:#ccc}
		.probe{width:100%;box-sizing:border-box}
		@media (min-width:700px){.desktop{background:#123;color:#fff}}
		@media (max-width:500px){.mobile{background:#fdd}}
		@media screen and (orientation:landscape){.landscape{background:#efe}}
	</style>
	<div class="probe">Viewport probe</div>
	<div class="desktop">Desktop rule</div>
	<div class="mobile">Mobile rule</div>
	<div class="landscape">Landscape rule</div>`
	_, layout := h.load("media-viewport", html)
	raster := h.rasterize("media-viewport")

	probe := h.boxAround(layout, "Viewport")
	desktop := h.boxAround(layout, "Desktop")
	mobile := h.boxAround(layout, "Mobile")
	landscape := h.boxAround(layout, "Landscape")
	page := h.pageBounds()
	if probe.W < page.W-90 || probe.X+probe.W > page.W {
		t.Fatalf("width:100%% did not fill the CSS viewport: probe=%+v page=%+v", probe, page)
	}
	h.requireFill(raster, desktop, sdl.Color{R: 0x11, G: 0x22, B: 0x33, A: 255}, 0.25, "min-width media rule")
	h.requireFill(raster, mobile, sdl.Color{R: 0xcc, G: 0xcc, B: 0xcc, A: 255}, 0.25, "non-matching max-width rule")
	h.requireFill(raster, landscape, sdl.Color{R: 0xee, G: 0xff, B: 0xee, A: 255}, 0.25, "orientation media rule")
	for _, text := range []string{"Viewport", "Desktop", "Mobile", "Landscape"} {
		h.requireTextPixels(raster, layout, text, "media query")
	}
	h.requireClearRightGutter(raster, "media queries")
}

func TestBrowserHeadlessFormControlStates(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		body{margin:0;font-size:17px}
		label{display:block;margin:6px 0 2px}
		input,textarea,select,button{display:block;width:260px;padding:6px;margin:6px 0;
			background:#fff;color:#111;border:1px solid #789}
		input:disabled,textarea:disabled,button:disabled{background:#ddd;color:#777}
	</style>
	<form action="/send" method="post">
		<label>Name</label><input name="name" value="Ada">
		<label>Locked field</label><input name="locked" value="Blocked" disabled>
		<label>Notes</label><textarea name="notes" disabled>Locked notes</textarea>
		<label>Choice</label><select name="choice"><option>One</option><option selected>Two</option></select>
		<label>Picks</label><input type="checkbox" name="agree" checked><input type="radio" name="pick">
		<button type="submit">Send</button>
		<button disabled>Cannot send</button>
	</form>`
	_, layout := h.load("form-states", html)
	raster := h.rasterize("form-states")

	enabled, enabledBox := headlessControlBox(t, layout, func(c *webControl) bool {
		return c.Kind == "text" && strings.Contains(c.Value, "Ada")
	})
	disabled, disabledBox := headlessControlBox(t, layout, func(c *webControl) bool {
		return c.Kind == "text" && strings.Contains(c.Value, "Blocked")
	})
	notes, notesBox := headlessControlBox(t, layout, func(c *webControl) bool {
		return c.Kind == "textarea" && strings.Contains(c.Value, "Locked notes")
	})
	choice, choiceBox := headlessControlBox(t, layout, func(c *webControl) bool {
		return c.Kind == "select"
	})
	submit, submitBox := headlessControlBox(t, layout, func(c *webControl) bool {
		return c.Kind == "submit" && strings.Contains(c.Label, "Send")
	})
	blocked, blockedBox := headlessControlBox(t, layout, func(c *webControl) bool {
		return c.Kind == "submit" && strings.Contains(c.Label, "Cannot")
	})
	if enabled == nil || disabled == nil || notes == nil || choice == nil || submit == nil || blocked == nil {
		t.Fatal("one or more form controls were not parsed")
	}
	if got := choice.Display(); !strings.Contains(got, "Two") {
		t.Fatalf("selected option was not displayed: %q", got)
	}
	if !headlessHasTextContaining(layout, "■") || !headlessHasTextContaining(layout, "○") {
		t.Fatalf("checkbox/radio glyphs are missing: runs=%v", headlessRunTexts(layout))
	}
	wantBackground := func(r sdl.Rect, c sdl.Color, label string) {
		h.requireFill(raster, r, c, 0.25, label)
	}
	wantBackground(enabledBox, sdl.Color{R: 255, G: 255, B: 255, A: 255}, "enabled text field")
	wantBackground(disabledBox, sdl.Color{R: 0xdd, G: 0xdd, B: 0xdd, A: 255}, "disabled text field")
	wantBackground(notesBox, sdl.Color{R: 0xdd, G: 0xdd, B: 0xdd, A: 255}, "disabled textarea")
	wantBackground(submitBox, sdl.Color{R: 255, G: 255, B: 255, A: 255}, "enabled submit")
	wantBackground(blockedBox, sdl.Color{R: 0xdd, G: 0xdd, B: 0xdd, A: 255}, "disabled submit")
	boxes := []sdl.Rect{enabledBox, disabledBox, notesBox, choiceBox, submitBox, blockedBox}
	for i, box := range boxes {
		h.requireInside("form control", box)
		if headlessRectOverlapsAny(box, append(append([]sdl.Rect{}, boxes[:i]...), boxes[i+1:]...)) {
			t.Fatalf("form controls overlap: %+v boxes=%v", box, boxes)
		}
	}
	for _, text := range []string{"Ada", "Blocked", "Locked notes", "Send"} {
		h.requireTextPixels(raster, layout, text, "form state")
	}
	h.requireInk(raster, h.textRectPrefix(layout, "Two"), "form select choice")
	h.requireInk(raster, h.textRectPrefix(layout, "Cannot"), "disabled submit")
	h.requireClearRightGutter(raster, "form states")
}
