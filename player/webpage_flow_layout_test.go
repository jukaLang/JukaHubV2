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
