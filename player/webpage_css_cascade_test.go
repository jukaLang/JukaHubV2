package main

import (
	"strings"
	"testing"
)

func TestWebCSSKeepsSemicolonInsideUnquotedURL(t *testing.T) {
	decls, important := parseWebDeclsSplit("background-image:url(data:image/svg+xml;base64,PHN2Zz4=); color:#123456")
	if got := decls["background-image"]; got != "url(data:image/svg+xml;base64,PHN2Zz4=)" {
		t.Fatalf("data URL was cut at its semicolon: %q", got)
	}
	if decls["color"] != "#123456" || len(important) != 0 {
		t.Fatalf("following declaration was lost: normal=%v important=%v", decls, important)
	}
}

func TestWebCSSImportantBeatsNormalInlineStyle(t *testing.T) {
	doc := parseWebPage(`<style>.notice { color: #c00000 !important }</style><p class="notice" style="color: #0000c0">Visible notice</p>`, "", "https://example.com/")
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if !strings.Contains(run.Text, "Visible notice") {
				continue
			}
			if !run.Style.HasColor || run.Style.Color.R != 0xc0 || run.Style.Color.G != 0 || run.Style.Color.B != 0 {
				t.Fatalf("normal inline style overrode !important colour: %+v", run.Style.Color)
			}
			return
		}
	}
	t.Fatal("the styled notice was not drawn")
}

func TestWebCSSInlineImportantBeatsStylesheetImportant(t *testing.T) {
	doc := parseWebPage(`<style>.notice { color: #c00000 !important }</style><p class="notice" style="color: #0000c0 !important">Visible notice</p>`, "", "https://example.com/")
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if !strings.Contains(run.Text, "Visible notice") {
				continue
			}
			if !run.Style.HasColor || run.Style.Color.R != 0 || run.Style.Color.G != 0 || run.Style.Color.B != 0xc0 {
				t.Fatalf("inline !important did not win: %+v", run.Style.Color)
			}
			return
		}
	}
	t.Fatal("the styled notice was not drawn")
}

func TestWebCSSBudgetKeepsCompleteNestedRules(t *testing.T) {
	large := "@media screen {.lead{color:#c00}" + strings.Repeat(" ", 200) + ".late{color:#00c}}"
	got := webCSSBudgetPrefix(large, 80)
	if len(got) > 80 || !strings.HasSuffix(got, "}}") {
		t.Fatalf("incomplete bounded CSS: %q", got)
	}
	rules := parseWebCSS(got)
	if len(rules) != 1 || rules[0].selector != ".lead" {
		t.Fatalf("complete rule in a large @media block was lost: %+v", rules)
	}
}

func TestWebCSSGridGroupsWholeCards(t *testing.T) {
	source := `<style>.cards{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:8px 20px}.card{width:180px}</style>` +
		`<div class="cards"><div class="card"><h2>One</h2><p>First card</p></div>` +
		`<div class="card"><h2>Two</h2><p>Second card</p></div>` +
		`<div class="card"><h2>Three</h2><p>Third card</p></div>` +
		`<div class="card"><h2>Four</h2><p>Fourth card</p></div></div>`
	doc := parseWebPage(source, "", "https://example.com/")
	var grid []webBlock
	for _, block := range doc.Blocks {
		if block.FlexGroup != 0 {
			grid = append(grid, block)
		}
	}
	items := webGroupFlexItems(grid)
	if len(items) != 4 {
		t.Fatalf("grid has %d items, want four cards: %+v", len(items), grid)
	}
	for i, item := range items {
		if len(item.blocks) != 2 {
			t.Fatalf("card %d split into %d cells, want one cell with two blocks", i, len(item.blocks))
		}
		for _, block := range item.blocks {
			if block.GridCols != 3 || block.FlexGapX != 20 || block.FlexGapY != 8 || block.Width != 180 {
				t.Fatalf("card %d lost grid sizing: cols=%d gap=(%g,%g) width=%g", i, block.GridCols, block.FlexGapX, block.FlexGapY, block.Width)
			}
		}
	}
	legibleTestInitFonts(t)
	layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 1280)
	type place struct {
		row int
		x   int32
	}
	positions := map[string]place{}
	for i, row := range layout.Rows {
		for _, run := range row.Runs {
			switch run.Text {
			case "One", "Two", "Three", "Four":
				positions[run.Text] = place{row: i, x: run.X}
			}
		}
	}
	a, b, c, d := positions["One"], positions["Two"], positions["Three"], positions["Four"]
	if len(positions) != 4 || a.row != b.row || b.row != c.row || !(a.x < b.x && b.x < c.x) ||
		d.row <= a.row || d.x != a.x {
		t.Fatalf("grid cards did not form three columns and a second row: %+v", positions)
	}
}

func TestWebCSSBackgroundImagePaintsOverFallbackColor(t *testing.T) {
	style := webBaseStyle()
	applyWebDecls(&style, map[string]string{
		"background-color": "#112233",
		"background-image": "linear-gradient(#cc0000, #0000cc)",
	})
	if !style.HasGradient || style.Gradient == nil || len(style.Gradient.Stops) != 2 {
		t.Fatal("fallback background color hid the gradient image")
	}
}

func TestWebCSSGapKeepsCalcAsOneValue(t *testing.T) {
	fields := webCSSSpaceFields("calc(8px + 4px) 20px")
	if len(fields) != 2 || fields[0] != "calc(8px + 4px)" || fields[1] != "20px" {
		t.Fatalf("gap fields = %q", fields)
	}
	style := webBaseStyle()
	applyWebLayoutDecls(&style, map[string]string{"gap": "8px 20px"})
	if style.GapY != 8 || style.GapX != 20 {
		t.Fatalf("CSS row/column gap reversed: y=%g x=%g", style.GapY, style.GapX)
	}
}
