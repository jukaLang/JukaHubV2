package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// gridAlignmentRects lays out a one-column, two-row grid with a definite
// 300px block size. The two row backgrounds are returned in row order.
func gridAlignmentRects(t *testing.T, alignContent, alignItems string) (sdl.Rect, sdl.Rect) {
	t.Helper()
	source := `<style>.grid{display:grid;grid-template-columns:100px;` +
		`grid-template-rows:40px 40px;gap:10px;height:300px;align-content:` + alignContent +
		`;align-items:` + alignItems + `}` +
		`.a{background:#cc0000}.b{background:#0000cc}</style>` +
		`<div class="grid"><div class="a">A</div><div class="b">B</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	a := gridRowTestRect(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	b := gridRowTestRect(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	return a, b
}

func TestWebGridAlignContentDistributesRowsInDefiniteHeight(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	base, _ := gridAlignmentRects(t, "start", "stretch")
	const (
		rowH       = int32(60)  // 40 CSS px at this reader's scale
		gap        = int32(15)  // 10 CSS px
		containerH = int32(450) // 300 CSS px
		free       = containerH - 2*rowH - gap
	)
	cases := []struct {
		name     string
		offset   int32
		rowDelta int32
	}{
		{"start", 0, rowH + gap},
		{"center", free / 2, rowH + gap},
		{"end", free, rowH + gap},
		{"space-between", 0, rowH + gap + free},
		{"space-around", free / int32(2) / 2, rowH + gap + free/2},
		{"space-evenly", free / 3, rowH + gap + free/3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, b := gridAlignmentRects(t, tc.name, "stretch")
			if got := a.Y - base.Y; got != tc.offset {
				t.Fatalf("first row offset = %d, want %d (first row %+v, baseline %+v)", got, tc.offset, a, base)
			}
			if got := b.Y - a.Y; got != tc.rowDelta {
				t.Fatalf("row delta = %d, want %d (first %+v, second %+v)", got, tc.rowDelta, a, b)
			}
		})
	}
}

func TestWebGridAlignContentStretchGrowsAutoRows(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.grid{display:grid;grid-template-columns:100px;` +
		`grid-template-rows:auto;height:300px;align-content:stretch;align-items:stretch}` +
		`.a{background:#cc0000}</style>` +
		`<div class="grid"><div class="a">A</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	a := gridRowTestRect(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	if a.H < 440 {
		t.Fatalf("align-content:stretch left the auto row at %d px high, want about the 450 px container height", a.H)
	}
}

func TestWebGridStretchKeepsExplicitItemHeight(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.grid{display:grid;grid-template-columns:300px;` +
		`grid-template-rows:800px;align-items:stretch}` +
		`.button{height:28px;background:#0c0c0c}</style>` +
		`<div class="grid"><div class="button">Download app</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	button := gridRowTestRect(t, layout, sdl.Color{R: 0x0c, G: 0x0c, B: 0x0c, A: 255})
	if button.H > 100 {
		t.Fatalf("explicit-height grid item stretched to %d px, want its 28px authored height", button.H)
	}
}

func TestWebFixedHeightContainerBackgroundDoesNotFollowOverflow(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<nav style="height:40px;background:#0c0c0c">` +
		`<p>First menu item</p><p>Second menu item</p><p>Third menu item</p>` +
		`</nav><h1>Page content</h1>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	nav := gridRowTestRect(t, layout, sdl.Color{R: 0x0c, G: 0x0c, B: 0x0c, A: 255})
	if nav.H > 80 {
		t.Fatalf("40px navigation background grew with its children to %d px", nav.H)
	}
}

func TestWebDisplayContentsChildrenJoinFlexRow(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.nav{display:flex}.wrapper{display:contents}` +
		`.item{width:80px}</style><nav class="nav">` +
		`<div class="wrapper"><div class="item">US</div>` +
		`<div class="item">World</div><div class="item">Politics</div></div></nav>`
	doc := parseWebPage(source, "", "https://example.com/")
	if len(doc.Blocks) != 3 {
		t.Fatalf("display:contents made %d blocks, want 3", len(doc.Blocks))
	}
	group := doc.Blocks[0].FlexGroup
	if group == 0 {
		t.Fatal("children lost their parent flex container")
	}
	for i, b := range doc.Blocks {
		if b.FlexGroup != group || b.FlexItem != i+1 {
			t.Fatalf("child %d is in flex group/item %d/%d, want %d/%d", i, b.FlexGroup, b.FlexItem, group, i+1)
		}
	}
	layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 800)
	positions := make(map[string]sdl.Point)
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			positions[run.Text] = sdl.Point{X: run.X, Y: row.Y}
		}
	}
	if positions["US"].Y != positions["World"].Y || positions["World"].Y != positions["Politics"].Y ||
		positions["US"].X >= positions["World"].X || positions["World"].X >= positions["Politics"].X {
		t.Fatalf("display:contents children did not lay out across a row: %+v", positions)
	}
}

func TestWebFixedHeightNavKeepsNowrapFlexItemsOnOneRow(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := `<style>.nav{height:40px}.items{display:flex}` +
		`.item{min-width:100px}</style>` +
		`<nav class="nav"><div class="items">` +
		`<div class="item">US</div><div class="item">World</div>` +
		`<div class="item">Politics</div><div class="item">Business</div>` +
		`<div class="item">Health</div><div class="item">Sports</div>` +
		`</div></nav><h1>News content</h1>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 400)
	positions := make(map[string]sdl.Point)
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			positions[run.Text] = sdl.Point{X: run.X, Y: row.Y}
		}
	}
	news, ok := positions["News"]
	if !ok || positions["US"].Y != positions["World"].Y || positions["World"].Y != positions["Politics"].Y ||
		news.Y > 200 {
		t.Fatalf("fixed-height nav expanded the flow: %+v", positions)
	}
}

func TestWebGridAlignItemsStayInsideShiftedRows(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	source := func(alignContent, alignItems string) string {
		return `<style>.grid{display:grid;grid-template-columns:100px 100px;` +
			`grid-template-rows:40px;height:300px;align-content:` + alignContent +
			`;align-items:` + alignItems + `}` +
			`.a{background:#cc0000}.b{background:#0000cc;align-self:start}</style>` +
			`<div class="grid"><div class="a">A</div><div class="b">B</div></div>`
	}
	layoutOf := func(alignContent, alignItems string) (sdl.Rect, sdl.Rect) {
		layout := parseWebPage(source(alignContent, alignItems), "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
		return gridRowTestRect(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255}),
			gridRowTestRect(t, layout, sdl.Color{R: 0, G: 0, B: 0xcc, A: 255})
	}

	baseA, _ := layoutOf("start", "start")
	centerA, startB := layoutOf("end", "center")
	if got := startB.Y - baseA.Y; got < 380 || got > 405 {
		t.Fatalf("align-content:end moved the row by %d px, want about 390", got)
	}
	if centerA.Y <= startB.Y {
		t.Fatalf("align-items:center did not centre inside the shifted row: centre=%+v start=%+v", centerA, startB)
	}
}
