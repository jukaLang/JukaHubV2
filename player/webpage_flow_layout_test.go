package main

import "testing"

func webTestTextPosition(t *testing.T, layout *webLayout, text string) (int, int32) {
	t.Helper()
	if layout == nil {
		t.Fatal("page layout is nil")
	}
	for rowIndex, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text == text {
				return rowIndex, run.X
			}
		}
	}
	t.Fatalf("text %q is missing from layout", text)
	return 0, 0
}

func TestWebFlexWrapRespectsCardBasis(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.cards{display:flex;flex-wrap:wrap;gap:12px}.card{flex:0 0 200px}</style>` +
		`<div class="cards"><div class="card">A</div><div class="card">B</div>` +
		`<div class="card">C</div><div class="card">D</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	aRow, aX := webTestTextPosition(t, layout, "A")
	bRow, bX := webTestTextPosition(t, layout, "B")
	cRow, cX := webTestTextPosition(t, layout, "C")
	dRow, dX := webTestTextPosition(t, layout, "D")
	if aRow != bRow || cRow != dRow || cRow <= aRow || !(aX < bX) || aX != cX || bX != dX {
		t.Fatalf("cards should wrap into two rows of two: A=(%d,%d) B=(%d,%d) C=(%d,%d) D=(%d,%d)",
			aRow, aX, bRow, bX, cRow, cX, dRow, dX)
	}
}

func TestWebFlexGrowExpandsItemsWithBasis(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.row{display:flex}.first{flex:1 1 160px}.second{flex:2 1 160px}</style>` +
		`<div class="row"><div class="first">A</div><div class="second">B</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	aRow, aX := webTestTextPosition(t, layout, "A")
	bRow, bX := webTestTextPosition(t, layout, "B")
	if aRow != bRow || bX-aX < 260 {
		t.Fatalf("the first growing item kept only its 160px basis: A=(%d,%d) B=(%d,%d)", aRow, aX, bRow, bX)
	}
}

func TestWebFlexGrowKeepsAutoSiblingOnRow(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.row{display:flex}.fill{flex:1}</style>` +
		`<div class="row"><div>Label</div><div class="fill">Result</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	labelRow, labelX := webTestTextPosition(t, layout, "Label")
	resultRow, resultX := webTestTextPosition(t, layout, "Result")
	if labelRow != resultRow || resultX <= labelX {
		t.Fatalf("auto sibling and growing item should share a row: Label=(%d,%d) Result=(%d,%d)",
			labelRow, labelX, resultRow, resultX)
	}
}

func TestWebNestedFlexStaysInsideParentCard(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.cards{display:flex;gap:20px}.card{width:200px}` +
		`.meta{display:flex;gap:8px}</style>` +
		`<div class="cards"><div class="card"><div class="meta"><div>A</div><div>B</div></div>` +
		`<p>Body</p></div><div class="card">C</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	aRow, aX := webTestTextPosition(t, layout, "A")
	bRow, bX := webTestTextPosition(t, layout, "B")
	bodyRow, _ := webTestTextPosition(t, layout, "Body")
	cRow, cX := webTestTextPosition(t, layout, "C")
	if aRow != bRow || aRow != cRow || bodyRow <= aRow || !(aX < bX && bX < cX) {
		t.Fatalf("nested row escaped its card: A=(%d,%d) B=(%d,%d) Body=%d C=(%d,%d)",
			aRow, aX, bRow, bX, bodyRow, cRow, cX)
	}
}

func TestWebNestedFlexInColumnKeepsRowGap(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.stack{display:flex;flex-direction:column;row-gap:30px}` +
		`.meta{display:flex}</style>` +
		`<div class="stack"><div class="meta"><div>A</div><div>B</div></div><div>C</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	aRow, _ := webTestTextPosition(t, layout, "A")
	bRow, _ := webTestTextPosition(t, layout, "B")
	cRow, _ := webTestTextPosition(t, layout, "C")
	if aRow != bRow || cRow < aRow+2 {
		t.Fatalf("column gap collapsed around a nested row: A=%d B=%d C=%d", aRow, bRow, cRow)
	}
}

func TestWebFlexDirectImagesAreSeparateItems(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.photos{display:flex;gap:12px}</style>` +
		`<div class="photos"><img src="https://example.com/a.png" width="100" height="50">` +
		`<img src="https://example.com/b.png" width="100" height="50"></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	if layout == nil || len(layout.Images) != 2 {
		t.Fatalf("image layout = %+v", layout)
	}
	a, b := layout.Images[0].Rect, layout.Images[1].Rect
	if a.Y != b.Y || a.X+a.W > b.X {
		t.Fatalf("direct flex images should share a row: a=%+v b=%+v", a, b)
	}
}

func TestWebFlexBasisOverridesWidthAndShorthand(t *testing.T) {
	l := &webLayouter{baseSize: 16}
	if got := l.itemWidth(webBlock{Width: 100, FlexBasis: 200}, 600); got != 200 {
		t.Fatalf("flex-basis should override width, got %d", got)
	}
	for _, value := range []string{"1", "1 1", "1 0", "1 1 0%"} {
		style := webStyle{FontPx: 16, FlexBasis: -1}
		webFlexShorthand(&style, value)
		if style.FlexGrow != 1 || style.FlexBasis != 0 {
			t.Errorf("flex %q = grow %g, basis %g; want grow 1, basis 0", value, style.FlexGrow, style.FlexBasis)
		}
	}
	style := webStyle{FontPx: 16, FlexBasis: -1}
	webFlexShorthand(&style, "initial")
	if style.FlexGrow != 0 || style.FlexBasis != -1 {
		t.Fatalf("flex: initial = grow %g, basis %g", style.FlexGrow, style.FlexBasis)
	}
}

func TestWebFlexItemsKeepBackgroundLayersAndVideos(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>` +
		`.row{display:flex;gap:20px}.card{flex:1}` +
		`.tinted{background-image:linear-gradient(#eef,#335)}` +
		`</style><div class="row">` +
		`<div class="card tinted">First</div>` +
		`<div class="card">Second<video src="https://example.com/movie.mp4"></video></div>` +
		`</div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	if layout == nil {
		t.Fatal("flex page produced no layout")
	}
	gradients := 0
	for _, bg := range layout.Backgrounds {
		if bg.HasGradient {
			gradients++
		}
	}
	if gradients == 0 {
		t.Fatal("a flex item's gradient background was dropped when its cell was merged into the page")
	}
	if len(layout.Videos) != 1 {
		t.Fatalf("a video inside a flex item was dropped: got %d video cards", len(layout.Videos))
	}
	firstRow, firstX := webTestTextPosition(t, layout, "First")
	secondRow, secondX := webTestTextPosition(t, layout, "Second")
	if firstRow != secondRow || firstX >= secondX {
		t.Fatalf("the two cards should share one row: First=(%d,%d) Second=(%d,%d)",
			firstRow, firstX, secondRow, secondX)
	}
}

// A block that paints a box of its own (a background, padding, a border) took
// its top inset twice: once when its box opened, once as its own leading
// margin (syncBoxes in webpage.go). A page whose first block was indented or
// painted started two blank rows down, and every such block later in the page
// was a row too tall.
func TestWebPaintedBlockTakesItsTopInsetOnce(t *testing.T) {
	legibleTestInitFonts(t)
	topAndGap := func(css string) (top, gap int) {
		layout := parseWebPage(`<style>.x{`+css+`}</style><p class="x">Alpha</p><p>After</p>`,
			"", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
		alpha, _ := webTestTextPosition(t, layout, "Alpha")
		after, _ := webTestTextPosition(t, layout, "After")
		return alpha, after - alpha
	}
	plainTop, plainGap := topAndGap("")
	for _, css := range []string{"padding-left:24px", "margin-left:24px", "background:#eee", "border-left:4px solid #000"} {
		top, gap := topAndGap(css)
		if top != plainTop {
			t.Fatalf("%s: the first paragraph started on row %d, want %d (a first block's top margin collapses at the page top)", css, top, plainTop)
		}
		if gap != plainGap {
			t.Fatalf("%s: the gap to the next paragraph was %d rows, want %d — the block's top inset was counted twice", css, gap, plainGap)
		}
	}
	quoted := parseWebPage(`<blockquote>Quoted words</blockquote><p>After</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	if row, _ := webTestTextPosition(t, quoted, "Quoted"); row != 0 {
		t.Fatalf("a blockquote opening the page started on row %d, want 0", row)
	}
}

// A table row numbers its cells from -1 (-(c+1) in webpage_table.go), so the
// first cell's blocks carry FlexItem -1 and the next cell's -2. Grouping a row
// into items has to open a group on its first block whatever number it carries:
// DuckDuckGo Lite's results page put such a row through a flex layout, the
// grouping read the -1 as a repeat of a group it had not opened yet, and
// indexed the empty list. The panic took the app down mid-draw, so the search
// results never appeared.
func TestWebGroupFlexItemsStartsOnNegativeCellNumber(t *testing.T) {
	visible := func(item int) webBlock {
		return webBlock{FlexItem: item, Runs: []webRun{{Text: "x"}}}
	}
	items := webGroupFlexItems([]webBlock{
		visible(-1), visible(-1), // the first cell's two blocks: one column
		visible(-2),            // the second cell: the next column
		visible(0), visible(0), // anonymous items keep their own columns
	})
	if len(items) != 4 {
		t.Fatalf("grouped %d items, want 4 (two cells, then two anonymous blocks)", len(items))
	}
	if len(items[0].blocks) != 2 || len(items[1].blocks) != 1 ||
		len(items[2].blocks) != 1 || len(items[3].blocks) != 1 {
		t.Fatalf("cells did not group by their number: %d/%d/%d/%d blocks",
			len(items[0].blocks), len(items[1].blocks), len(items[2].blocks), len(items[3].blocks))
	}
}

// The same row reaching the layout through a flex container, as DuckDuckGo
// Lite's results page does: the first cell's FlexItem is -1 and the row must
// still draw both its columns.
func TestWebFlexRowWithTableCellsLaysOut(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<div style="display:flex"><table><tr><td>1.</td><td>Title</td></tr>`+
		`<tr><td></td><td>Snippet</td></tr></table></div>`,
		"", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	titleRow, titleX := webTestTextPosition(t, layout, "Title")
	snippetRow, snippetX := webTestTextPosition(t, layout, "Snippet")
	if titleRow == snippetRow || titleX != snippetX {
		t.Fatalf("the table's rows should stack with their columns aligned: Title=(%d,%d) Snippet=(%d,%d)",
			titleRow, titleX, snippetRow, snippetX)
	}
}
