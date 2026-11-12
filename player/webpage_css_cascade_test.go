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

func webTestStyleForText(doc *webDocument, text string) (webStyle, bool) {
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if strings.Contains(run.Text, text) {
				return run.Style, true
			}
		}
	}
	return webStyle{}, false
}

func TestWebFunctionalPseudoClassMatching(t *testing.T) {
	cases := []struct {
		name   string
		source string
		text   string
		red    bool
	}{
		{"is hit", `<style>.item:is(.a,.b){color:#cc0000}</style><p class="item b">Hit</p>`, "Hit", true},
		{"is miss", `<style>.item:is(.a,.b){color:#cc0000}</style><p class="item c">Miss</p>`, "Miss", false},
		{"is complex hit", `<style>.item:is(.a .b){color:#cc0000}</style><div class="a"><p class="item b">Hit</p></div>`, "Hit", true},
		{"is complex miss", `<style>.item:is(.a .b){color:#cc0000}</style><p class="item b">Miss</p>`, "Miss", false},
		{"where hit", `<style>.item:where(.a,.b){color:#cc0000}</style><p class="item b">Hit</p>`, "Hit", true},
		{"not list hit", `<style>.item:not(.a,.b){color:#cc0000}</style><p class="item c">Hit</p>`, "Hit", true},
		{"not list miss", `<style>.item:not(.a,.b){color:#cc0000}</style><p class="item a">Miss</p>`, "Miss", false},
		{"not nested is hit", `<style>.item:not(:is(.a,.b)){color:#cc0000}</style><p class="item c">Hit</p>`, "Hit", true},
		{"not nested is miss", `<style>.item:not(:is(.a,.b)){color:#cc0000}</style><p class="item a">Miss</p>`, "Miss", false},
		{"has child hit", `<style>.card:has(> img){color:#cc0000}</style><div class="card"><span>Hit</span><img src="x.png"></div>`, "Hit", true},
		{"has child miss", `<style>.card:has(> .missing){color:#cc0000}</style><div class="card"><span>Miss</span></div>`, "Miss", false},
		{"has descendant hit", `<style>.card:has(img){color:#cc0000}</style><div class="card"><div><span>Hit</span><img src="x.png"></div></div>`, "Hit", true},
		{"anchor has descendant", `<style>a:has(img){color:#cc0000}</style><a href="#"><span>Hit</span><img src="x.png"></a>`, "Hit", true},
		{"anchor has child miss", `<style>a:has(> .missing){color:#cc0000}</style><a href="#"><span>Miss</span></a>`, "Miss", false},
		{"has next sibling hit", `<style>.first:has(+ .second){color:#cc0000}</style><div class="first">Hit</div><div class="second">Next</div>`, "Hit", true},
		{"has later sibling hit", `<style>.first:has(~ .second){color:#cc0000}</style><div class="first">Hit</div><div class="middle">Middle</div><div class="second">Later</div>`, "Hit", true},
		{"nested is and has", `<style>.card:is(.missing,.card:has(> img)){color:#cc0000}</style><div class="card"><span>Hit</span><img src="x.png"></div>`, "Hit", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseWebPage(tc.source, "", "https://example.com/")
			style, ok := webTestStyleForText(doc, tc.text)
			if !ok {
				t.Fatalf("text %q was not drawn", tc.text)
			}
			gotRed := style.HasColor && style.Color.R == 0xcc && style.Color.G == 0 && style.Color.B == 0
			if gotRed != tc.red {
				t.Fatalf("colour %+v / has=%v, want red=%v", style.Color, style.HasColor, tc.red)
			}
		})
	}
}

func TestWebFunctionalPseudoClassSpecificity(t *testing.T) {
	source := `<style>` +
		`.target:where(#x){color:#cc0000}` +
		`.target{color:#0000cc}` +
		`</style><p class="target" id="x">Where</p>`
	doc := parseWebPage(source, "", "https://example.com/")
	style, ok := webTestStyleForText(doc, "Where")
	if !ok {
		t.Fatal("where target was not drawn")
	}
	if !style.HasColor || style.Color.R != 0 || style.Color.G != 0 || style.Color.B != 0xcc {
		t.Fatalf(":where() added specificity: %+v", style.Color)
	}

	source = `<style>` +
		`.target:is(#x,.other){color:#cc0000}` +
		`.target{color:#0000cc}` +
		`</style><p class="target" id="x">Is</p>`
	doc = parseWebPage(source, "", "https://example.com/")
	style, ok = webTestStyleForText(doc, "Is")
	if !ok {
		t.Fatal("is target was not drawn")
	}
	if !style.HasColor || style.Color.R != 0xcc || style.Color.G != 0 || style.Color.B != 0 {
		t.Fatalf(":is() lost its most specific argument: %+v", style.Color)
	}
}

func TestWebFunctionalPseudoClassMalformedInput(t *testing.T) {
	for _, text := range []string{
		":is(", ":not()", ":has()", ":is(.a, :not())", ":has(> )",
		":is(.a, :has(.x))", ":not(:is(.a, .b))", ":has(:not(.a, .b))",
	} {
		if sel, _ := webCompileSelector(text); sel == nil {
			t.Fatalf("%q returned a nil selector", text)
		}
	}
	if !webSupportsApplies("@supports selector(.card:has(> img))") {
		t.Fatal("@supports selector() rejected :has()")
	}
}

func TestWebSupportsConditions(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"@supports (display:grid)", true},
		{"@supports not (display:grid)", false},
		{"@supports (display:grid) and (gap:1rem)", true},
		{"@supports (display:grid) and (display:made-up)", false},
		{"@supports (display:grid) or (unknown-property: 1)", true},
		{"@supports (unknown-property: 1)", false},
		{"@supports selector(.card)", true},
		{"@supports (not (display:grid))", false},
		{"@supports (width: 10px)", true},
		{"@supports (width: made-up)", false},
		{"@supports (color: #123456)", true},
		{"@supports (color: not-a-colour)", false},
		{"@supports (grid-template-columns: repeat(2, 1fr))", true},
		{"@supports (grid-template-columns: made-up)", false},
		{"@supports (font-size: large-nonsense)", false},
		{"@supports (display: contents)", false},
		{"@supports (width: auto)", true},
		{"@supports (gap: 1rem)", true},
		{"@supports (gap: made-up)", false},
		{"@supports (text-align: made-up)", false},
		{"@supports (font-style: made-up)", false},
		{"@supports (margin-top: made-up)", false},
		{"@supports (border: made-up)", false},
		{"@supports (border: 1px solid #123456)", true},
		{"@supports (line-height: made-up)", false},
		{"@supports (opacity: made-up)", false},
		{"@supports (grid-template-columns: repeat(nope, 1fr))", false},
		{"@supports (aspect-ratio: 16 / 9)", true},
		{"@supports (width: 0)", true},
		{"@supports (margin: 0)", true},
		{"@supports (opacity: 0)", true},
		{"@supports (color: 0)", false},
		{"@supports (display: 0)", false},
		{"@supports (grid-template-columns: none)", true},
		{"@supports (color: #123456 !important)", true},
		{"@supports (font-style: oblique 10deg)", true},
		{"@supports (border-color: red blue)", true},
		{"@supports (background: made-up)", false},
		{"@supports (font: made-up)", false},
		{"@supports (flex: made-up)", false},
	}
	for _, tc := range cases {
		if got := webSupportsApplies(tc.query); got != tc.want {
			t.Errorf("webSupportsApplies(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestWebCSSSupportsChoosesSupportedFallback(t *testing.T) {
	source := `<style>` +
		`.notice{color:#111111}` +
		`@supports (display:grid){.notice{color:#cc0000}}` +
		`@supports (display:made-up){.notice{color:#0000cc}}` +
		`@supports not (display:grid){.notice{color:#00cc00}}` +
		`</style><p class="notice">Visible notice</p>`
	doc := parseWebPage(source, "", "https://example.com/")
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if !strings.Contains(run.Text, "Visible notice") {
				continue
			}
			if !run.Style.HasColor || run.Style.Color.R != 0xcc || run.Style.Color.G != 0 || run.Style.Color.B != 0 {
				t.Fatalf("supported @supports block did not win: %+v", run.Style.Color)
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

func TestWebContainerQueryRules(t *testing.T) {
	cases := []struct {
		name  string
		query string
		html  string
		red   bool
	}{
		{
			name:  "inline-size hit",
			query: `@container (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;width:600px"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "inline-size miss",
			query: `@container (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;width:320px"><p class="card">Card</p></div>`,
		},
		{
			name:  "stylesheet container class hit",
			query: `.box{container-type:inline-size;width:600px}@container (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div class="box"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "named hit",
			query: `@container panel (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="container:panel / inline-size;width:600px"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "named longhand hit",
			query: `@container panel (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="container-name:panel;container-type:inline-size;width:600px"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "named mismatch",
			query: `@container panel (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="container:other / inline-size;width:600px"><p class="card">Card</p></div>`,
		},
		{
			name:  "outer named container wins",
			query: `@container outer (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="container:outer / inline-size;width:600px"><div style="container:inner / inline-size;width:300px"><p class="card">Card</p></div></div>`,
			red:   true,
		},
		{
			name:  "no container",
			query: `@container (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="width:600px"><p class="card">Card</p></div>`,
		},
		{
			name:  "normal container is not size queryable",
			query: `@container (min-width:1px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:normal;width:600px"><p class="card">Card</p></div>`,
		},
		{
			name:  "height hit on size container",
			query: `@container (min-height:300px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:size;height:400px"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "height miss on inline-size container",
			query: `@container (min-height:300px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;height:400px"><p class="card">Card</p></div>`,
		},
		{
			name:  "range syntax hit",
			query: `@container (width >= 500px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;width:600px"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "logical inline-size hit",
			query: `@container (inline-size > 400px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;width:600px"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "compound and hit",
			query: `@container (min-width:500px) and (max-width:700px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;width:600px"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "compound and miss",
			query: `@container (min-width:500px) and (max-width:550px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;width:600px"><p class="card">Card</p></div>`,
		},
		{
			name:  "nested container rules",
			query: `@container (min-width:500px){@container (max-width:700px){.card{color:#cc0000}}}`,
			html:  `<div style="container-type:inline-size;width:600px"><p class="card">Card</p></div>`,
			red:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := `<style>.card{color:#111111}` + tc.query + `</style>` + tc.html
			doc := parseWebPage(source, "", "https://example.com/")
			var found bool
			for _, block := range doc.Blocks {
				for _, run := range block.Runs {
					if !strings.Contains(run.Text, "Card") {
						continue
					}
					found = true
					if !run.Style.HasColor {
						t.Fatalf("card has no parsed color: %+v", run.Style)
					}
					gotRed := run.Style.Color.R == 0xcc && run.Style.Color.G == 0 && run.Style.Color.B == 0
					if gotRed != tc.red {
						t.Fatalf("card color = %v, want red=%v (%+v)", run.Style.Color, tc.red, run.Style)
					}
					return
				}
			}
			if !found {
				t.Fatal("the card text was not drawn")
			}
		})
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
