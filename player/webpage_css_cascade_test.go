package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

func TestWebStructuralPseudoClassMatching(t *testing.T) {
	cases := []struct {
		name   string
		source string
		text   string
		red    bool
	}{
		{"first child hit among text and comments", `<style>.item:first-child{color:#cc0000}</style><div>text<!--c--><span class="item">Hit</span><span>Second</span></div>`, "Hit", true},
		{"first child miss", `<style>.item:first-child{color:#cc0000}</style><div><span>First</span><span class="item">Miss</span></div>`, "Miss", false},
		{"last child hit", `<style>.item:last-child{color:#cc0000}</style><div><span>First</span><span class="item">Hit</span></div>`, "Hit", true},
		{"last child miss", `<style>.item:last-child{color:#cc0000}</style><div><span class="item">Miss</span><span>Last</span></div>`, "Miss", false},
		{"nth child hit", `<style>.item:nth-child(2){color:#cc0000}</style><ul><li>One</li><li class="item">Hit</li><li>Three</li></ul>`, "Hit", true},
		{"nth child miss", `<style>.item:nth-child(2){color:#cc0000}</style><ul><li class="item">Miss</li><li>Two</li></ul>`, "Miss", false},
		{"nth child odd", `<style>.item:nth-child(odd){color:#cc0000}</style><div><i>One</i><i>Two</i><i class="item">Hit</i></div>`, "Hit", true},
		{"nth child even", `<style>.item:nth-child(even){color:#cc0000}</style><div><i>One</i><i class="item">Hit</i><i>Three</i></div>`, "Hit", true},
		{"nth child 2n+1", `<style>.item:nth-child(2n+1){color:#cc0000}</style><div><i>One</i><i>Two</i><i class="item">Hit</i></div>`, "Hit", true},
		{"nth child negative range", `<style>.item:nth-child(-n+2){color:#cc0000}</style><div><i>One</i><i class="item">Hit</i><i>Three</i></div>`, "Hit", true},
		{"nth child negative range miss", `<style>.item:nth-child(-n+2){color:#cc0000}</style><div><i>One</i><i>Two</i><i class="item">Miss</i></div>`, "Miss", false},
		{"nth child inside has", `<style>.card:has(> .item:nth-child(2)){color:#cc0000}</style><div class="card"><span>Hit</span><i class="item"></i></div>`, "Hit", true},
		{"nth last child hit", `<style>.item:nth-last-child(2){color:#cc0000}</style><div><i>One</i><i class="item">Hit</i><i>Three</i></div>`, "Hit", true},
		{"nth last child miss", `<style>.item:nth-last-child(1){color:#cc0000}</style><div><i class="item">Miss</i><i>Two</i></div>`, "Miss", false},
		{"nth last child odd", `<style>.item:nth-last-child(odd){color:#cc0000}</style><div><i class="item">Hit</i><i>Two</i><i>Three</i></div>`, "Hit", true},
		{"nth of type ignores other tags", `<style>.item:nth-of-type(2){color:#cc0000}</style><div><span>Heading</span><i>One</i><i class="item">Hit</i></div>`, "Hit", true},
		{"nth of type miss", `<style>.item:nth-of-type(2){color:#cc0000}</style><div><i class="item">Miss</i><i>Two</i></div>`, "Miss", false},
		{"nth of type formula", `<style>.item:nth-of-type(2n+1){color:#cc0000}</style><div><i class="item">Hit</i><i>Two</i><i>Three</i></div>`, "Hit", true},
		{"nth last of type hit", `<style>.item:nth-last-of-type(2){color:#cc0000}</style><div><span>Heading</span><i class="item">Hit</i><i>Second</i></div>`, "Hit", true},
		{"nth last of type miss", `<style>.item:nth-last-of-type(2){color:#cc0000}</style><div><i>One</i><i class="item">Miss</i></div>`, "Miss", false},
		{"nth child of class", `<style>.item:nth-child(2 of .active){color:#cc0000}</style><div><i class="active">One</i><i>Skip</i><i class="active item">Hit</i></div>`, "Hit", true},
		{"nth child of class miss", `<style>.item:nth-child(2 of .active){color:#cc0000}</style><div><i class="active">One</i><i class="item">Miss</i><i>Three</i></div>`, "Miss", false},
		{"nth last child of class", `<style>.item:nth-last-child(1 of .active){color:#cc0000}</style><div><i class="active">One</i><i>Skip</i><i class="active item">Hit</i></div>`, "Hit", true},
		{"nth of type of class", `<style>.item:nth-of-type(2 of .active){color:#cc0000}</style><div><i class="active">One</i><i>Skip</i><i class="active item">Hit</i></div>`, "Hit", true},
		{"nth last of type of class", `<style>.item:nth-last-of-type(1 of .active){color:#cc0000}</style><div><i class="active">One</i><i>Skip</i><i class="active item">Hit</i></div>`, "Hit", true},
		{"nth child of tag", `<style>.item:nth-child(2 of i){color:#cc0000}</style><div><span>Heading</span><i>One</i><i class="item">Hit</i></div>`, "Hit", true},
		{"nth last child of tag", `<style>.item:nth-last-child(1 of i){color:#cc0000}</style><div><span>Heading</span><i>One</i><i class="item">Hit</i></div>`, "Hit", true},
		{"nth of type of tag", `<style>.item:nth-of-type(2 of i){color:#cc0000}</style><div><span>Heading</span><i>One</i><i class="item">Hit</i></div>`, "Hit", true},
		{"nth last of type of tag", `<style>.item:nth-last-of-type(2 of i){color:#cc0000}</style><div><span>Heading</span><i>One</i><i class="item">Hit</i><i>Three</i></div>`, "Hit", true},
		{"nth child of nested list", `<style>.item:nth-child(2 of :is(.active, .other)){color:#cc0000}</style><div><i class="active">One</i><i>Skip</i><i class="active item">Hit</i></div>`, "Hit", true},
		{"nth child of selector list", `<style>.item:nth-child(2 of .active, .other){color:#cc0000}</style><div><i class="other">One</i><i>Skip</i><i class="active item">Hit</i></div>`, "Hit", true},
		{"nth child of class formula", `<style>.item:nth-child(2n of .active){color:#cc0000}</style><div><i class="active">One</i><i>Skip</i><i class="active item">Hit</i></div>`, "Hit", true},
		{"li nth child of class formula", `<style>li:nth-child(2n of .active){color:#cc0000}</style><ul><li class="active">One</li><li>Skip</li><li class="active item">Hit</li></ul>`, "Hit", true},
		{"first child with explicit head", `<html><head><style>.item:first-child{color:#cc0000}</style></head><body><div><span class="item">Hit</span><span>Second</span></div></body></html>`, "Hit", true},
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

func TestWebNthChildOfClassFiltersSiblings(t *testing.T) {
	doc := parseWebPage(`<style>li:nth-child(2n of .active){color:#cc0000}</style>`+
		`<ul><li class="active">One</li><li>Skip</li><li class="active">Hit</li></ul>`, "", "https://example.com/")
	one, ok := webTestStyleForText(doc, "One")
	if !ok {
		t.Fatal("One was not drawn")
	}
	hit, ok := webTestStyleForText(doc, "Hit")
	if !ok {
		t.Fatal("Hit was not drawn")
	}
	red := func(style webStyle) bool {
		return style.HasColor && style.Color.R == 0xcc && style.Color.G == 0 && style.Color.B == 0
	}
	if red(one) {
		t.Fatal("the first active li matched 2n despite being filtered position 1")
	}
	if !red(hit) {
		t.Fatal("the second active li did not match 2n of .active")
	}
}

func TestWebGeneratedContentPseudoElements(t *testing.T) {
	source := `<style>` +
		`.price::before{content:"$"}` +
		`.price::after{content:" USD"}` +
		`.price{color:#123456}` +
		`.badge::before{content:attr(data-label) ": "}` +
		`.badge{color:#654321}` +
		`.skip::before{content:"SKIP"}` +
		`.skip::before{content:none}` +
		`.old:before{content:"[old]"}` +
		`.escaped::before{content:"\2192  Arrow"}` +
		`.hidden::after{content:"HIDDEN";display:none}` +
		`li::marker{content:"* "}` +
		`.red::before{content:"!";color:#cc0000}` +
		`</style>` +
		`<p class="price">9.99</p>` +
		`<p class="badge" data-label="New &amp; Hot">Item</p>` +
		`<p class="skip">Plain</p>` +
		`<p class="old">Old</p>` +
		`<p class="escaped">Arrow</p>` +
		`<p class="hidden">Tail</p>` +
		`<ul><li>First</li><li>Second</li></ul>` +
		`<p class="red">Text</p>`
	doc := parseWebPage(source, "", "https://example.com/")
	var all strings.Builder
	for _, block := range doc.Blocks {
		all.WriteString(block.Bullet)
		for _, run := range block.Runs {
			all.WriteString(run.Text)
		}
	}
	got := all.String()
	for _, want := range []string{"$9.99 USD", "New & Hot: Item", "[old]Old", "→ Arrow"} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated text %q missing from %q", want, got)
		}
	}
	if strings.Contains(got, "SKIP") {
		t.Fatalf("content:none still generated text: %q", got)
	}
	if strings.Contains(got, "HIDDEN") {
		t.Fatalf("a hidden pseudo-element generated text: %q", got)
	}
	if strings.Count(got, "* ") < 2 {
		t.Fatalf("::marker content did not replace both bullets: %q", got)
	}
	// The pseudo-element's colour must reach its own text and not the
	// originating element's text.
	mark, ok := webTestStyleForText(doc, "!")
	if !ok || !mark.HasColor || mark.Color.R != 0xcc || mark.Color.G != 0 || mark.Color.B != 0 {
		t.Fatalf("::before did not take its own colour: %+v", mark.Color)
	}
	text, ok := webTestStyleForText(doc, "Text")
	if !ok || text.HasColor && text.Color.R == 0xcc && text.Color.G == 0 && text.Color.B == 0 {
		t.Fatalf("::before colour leaked onto the element: %+v", text.Color)
	}
}

func TestWebGeneratedContentParsing(t *testing.T) {
	if sel, _ := webCompileSelector(".x::before"); sel.never || sel.pseudo != webPseudoElemBefore {
		t.Fatalf("::before was not recorded: never=%v pseudo=%d", sel.never, sel.pseudo)
	}
	if sel, _ := webCompileSelector(".x:after"); sel.never || sel.pseudo != webPseudoElemAfter {
		t.Fatalf("the single-colon :after spelling was not recorded: never=%v pseudo=%d", sel.never, sel.pseudo)
	}
	if sel, _ := webCompileSelector("li::marker"); sel.never || sel.pseudo != webPseudoElemMarker {
		t.Fatalf("::marker was not recorded: never=%v pseudo=%d", sel.never, sel.pseudo)
	}
	for _, text := range []string{".x::first-line", ".x::selection", ".x::before::after", ".x:not(::before)"} {
		if sel, _ := webCompileSelector(text); !sel.never {
			t.Fatalf("%q should not match an element", text)
		}
	}
	if got, ok := webGeneratedContent(`"A" attr(data-x) "B"`, `p data-x="Y"`, nil); !ok || got != "AYB" {
		t.Fatalf("content strings and attr() resolved to %q, ok=%v", got, ok)
	}
	if got, ok := webGeneratedContent(`none`, "", nil); !ok || got != "" {
		t.Fatalf("content:none resolved to %q, ok=%v", got, ok)
	}
	if _, ok := webGeneratedContent(`normal`, "", nil); ok {
		t.Fatal("content:normal should ask for the default marker")
	}
}

func TestWebCSSNestedCountersAndMarkerStyle(t *testing.T) {
	source := `<style>` +
		`ol{counter-reset:section;list-style:none}` +
		`li{counter-increment:section}` +
		`li::before{content:counters(section,".") " "}` +
		`li::marker{content:counter(section,upper-roman) ". ";color:#cc0000;font-size:24px}` +
		`ol::after{content:" [" counter(section) "]"}` +
		`</style>` +
		`<ol><li>One</li><li>Two<ol><li>Nested</li><li>Deep</li></ol></li><li>Three</li></ol>`
	doc := parseWebPage(source, "", "https://example.com/")
	var all strings.Builder
	var markerSeen bool
	var markerStyle webStyle
	for _, block := range doc.Blocks {
		all.WriteString(block.Bullet)
		all.WriteString(" ")
		for _, run := range block.Runs {
			all.WriteString(run.Text)
		}
		all.WriteString("\n")
		if block.Bullet != "" && block.HasMarkerStyle {
			markerSeen = true
			markerStyle = block.MarkerStyle
		}
	}
	got := all.String()
	for _, want := range []string{"1 One", "2 Two", "2.1 Nested", "2.2 Deep", "3 Three", "[3]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("nested counter output %q missing from %q", want, got)
		}
	}
	if !markerSeen {
		t.Fatalf("::marker style was not attached to a list block: %q", got)
	}
	if !markerStyle.HasColor || markerStyle.Color.R != 0xcc || markerStyle.Color.G != 0 || markerStyle.Color.B != 0 {
		t.Fatalf("::marker colour was not resolved: %+v", markerStyle.Color)
	}
	if markerStyle.FontPx != 24 {
		t.Fatalf("::marker font-size was not resolved: %v", markerStyle.FontPx)
	}
}

func TestWebCSSMarkerDefaultBulletStyle(t *testing.T) {
	source := `<style>li::marker{color:#cc0000;font-size:20px}</style><ul><li>Item</li></ul>`
	doc := parseWebPage(source, "", "https://example.com/")
	for _, block := range doc.Blocks {
		if block.Bullet == "" {
			continue
		}
		if !block.HasMarkerStyle || block.MarkerStyle.FontPx != 20 || block.MarkerStyle.Color.R != 0xcc {
			t.Fatalf("default bullet did not take ::marker style: %+v", block.MarkerStyle)
		}
		return
	}
	t.Fatal("the list bullet was not laid out")
}

func TestWebCSSContentCounterStyles(t *testing.T) {
	counters := map[string][]int{"n": {3}}
	cases := map[string]string{
		`counter(n)`:                       "3",
		`counter(n, decimal-leading-zero)`: "03",
		`counter(n, lower-alpha)`:          "c",
		`counter(n, upper-alpha)`:          "C",
		`counter(n, lower-roman)`:          "iii",
		`counter(n, upper-roman)`:          "III",
	}
	for raw, want := range cases {
		if got, ok := webGeneratedContent(raw, "", counters); !ok || got != want {
			t.Fatalf("%s resolved to %q, ok=%v, want %q", raw, got, ok, want)
		}
	}
}

func TestWebCSSLetterAndWordSpacingChangeLayout(t *testing.T) {
	legibleTestInitFonts(t)
	findX := func(layout *webLayout, prefix string) int32 {
		t.Helper()
		for _, row := range layout.Rows {
			for _, run := range row.Runs {
				if strings.HasPrefix(run.Text, prefix) {
					return run.X
				}
			}
		}
		t.Fatalf("no run starts with %q", prefix)
		return 0
	}
	baseLayout := parseWebPage(`<p><span>AB</span> <span>CD</span></p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	spacedLayout := parseWebPage(`<style>p{letter-spacing:10px;word-spacing:20px}</style><p><span>AB</span> <span>CD</span></p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	baseA, baseC := findX(baseLayout, "A"), findX(baseLayout, "C")
	spacedA, spacedC := findX(spacedLayout, "A"), findX(spacedLayout, "C")
	if spacedC-spacedA <= baseC-baseA+15 {
		t.Fatalf("spacing did not advance the second word: base %d->%d, spaced %d->%d", baseA, baseC, spacedA, spacedC)
	}
	style, ok := webTestStyleForText(parseWebPage(`<style>p{letter-spacing:10px;word-spacing:20px}</style><p>Styled</p>`, "", "https://example.com/"), "Styled")
	if !ok || style.LetterSpacing != 10 || style.WordSpacing != 20 {
		t.Fatalf("spacing was not parsed into the run style: %+v", style)
	}
}

func TestWebCSSOutlineAndDecorationColor(t *testing.T) {
	legibleTestInitFonts(t)
	doc := parseWebPage(`<style>.box{outline:3px solid #cc0000}</style><div class="box">Boxed</div>`, "", "https://example.com/")
	var boxed bool
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if run.Text == "Boxed" {
				boxed = true
				if !block.Outline || block.BoxBorder || block.BorderWidth != 3 || !block.HasBorderColor || block.BorderColor.R != 0xcc {
					t.Fatalf("outline did not paint a box edge: %+v", block)
				}
			}
		}
	}
	if !boxed {
		t.Fatal("the outlined box was not laid out")
	}
	doc = parseWebPage(`<style>a{text-decoration:underline;text-decoration-color:#cc0000}</style><a href="#x">Link</a>`, "", "https://example.com/")
	style, ok := webTestStyleForText(doc, "Link")
	if !ok || !style.HasDecorationColor || style.DecorationColor.R != 0xcc || !style.Underline {
		t.Fatalf("text-decoration-color was not resolved: %+v", style)
	}
	layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 800)
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text == "Link" {
				if !run.HasDecorationColor || run.DecorationColor.R != 0xcc {
					t.Fatalf("decoration colour was not carried into paint state: %+v", run)
				}
				return
			}
		}
	}
	t.Fatal("the decorated link was not laid out")
}

func TestWebCSSTextShadowRendersOffsetAndColor(t *testing.T) {
	doc := parseWebPage(`<style>.s{text-shadow:2px 3px #cc0000}</style><p class="s">Shadow</p>`, "", "https://example.com/")
	style, ok := webTestStyleForText(doc, "Shadow")
	if !ok || !style.HasTextShadow || style.TextShadowX != 2 || style.TextShadowY != 3 || !style.HasTextShadowColor || style.TextShadowColor.R != 0xcc {
		t.Fatalf("text-shadow was not resolved: %+v", style)
	}
	layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 800)
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text != "Shadow" {
				continue
			}
			if !run.HasShadow || run.ShadowColor.R != 0xcc {
				t.Fatalf("text-shadow was not carried into paint state: %+v", run)
			}
			return
		}
	}
	t.Fatal("the shadowed text was not laid out")
}

func TestWebCSSCounterSetChangesRenderedValue(t *testing.T) {
	source := `<style>` +
		`ol{counter-reset:n;list-style:none}` +
		`li{counter-increment:n}` +
		`li::before{content:counter(n) " "}` +
		`.set{counter-set:n 10}` +
		`</style><ol><li>One</li><li class="set">Ten</li><li>Eleven</li></ol>`
	doc := parseWebPage(source, "", "https://example.com/")
	var all strings.Builder
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			all.WriteString(run.Text)
		}
	}
	got := all.String()
	for _, want := range []string{"1 One", "10 Ten", "11 Eleven"} {
		if !strings.Contains(got, want) {
			t.Fatalf("counter-set output %q missing from %q", want, got)
		}
	}
}

func TestWebCSSBoxShadowPaintsOffsetRect(t *testing.T) {
	if x, y, _, ok := webShadowValue("0px 4px #cc0000", 16); !ok || x != 0 || y != 4 {
		t.Fatalf("zero horizontal offset dropped the shadow: x=%v y=%v ok=%v", x, y, ok)
	}
	doc := parseWebPage(`<style>.card{box-shadow:4px 6px #cc0000}</style><div class="card">Card</div>`, "", "https://example.com/")
	style, ok := webTestStyleForText(doc, "Card")
	if !ok || !style.HasBoxShadow || style.BoxShadowX != 4 || style.BoxShadowY != 6 || !style.HasBoxShadowColor || style.BoxShadowColor.R != 0xcc {
		t.Fatalf("box-shadow was not resolved: %+v", style)
	}
	layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 800)
	for _, rect := range layout.Rects {
		if rect.Color.R == 0xcc && rect.Color.G == 0 && rect.Color.B == 0 && rect.Rect.W > 0 && rect.Rect.H > 0 {
			return
		}
	}
	t.Fatal("box-shadow did not produce a painted rectangle")
}

func TestWebCSSUnsupportedConstructsAreDocumented(t *testing.T) {
	data, err := os.ReadFile("CSS_SUPPORT.md")
	if err != nil {
		t.Fatalf("CSS support contract is missing: %v", err)
	}
	doc := string(data)
	for _, construct := range []string{
		"font-family", "Percentage heights",
		"aspect-ratio on an ordinary block",
		"block/inline conversion", "Width/min-width/max-width on a flex/grid container",
		"position: sticky",
		"text-overflow", "transform",
		"transition", "animation", "filter", "clip-path", "grid-template-rows",
		"grid-row", "grid-column", "grid-area", "white-space",
		"list-style-image", "list-style-position",
		"::first-line", "::first-letter", "::selection", "::placeholder", "::backdrop",
		"content: url", "@font-face", "@keyframes", "@page", "@layer", "@import",
		"text-align: justify",
	} {
		if !strings.Contains(doc, construct) {
			t.Fatalf("unsupported CSS %q is not documented in CSS_SUPPORT.md", construct)
		}
	}
}

// TestWebCSSAcceptedPropertiesHaveCoverage is the audit's tripwire. It reads
// the cascade's own decls[...] lookups from webpage.go and fails when a new
// property is accepted without being classified here. Every classified name
// is rendered by a TestWeb test or listed in CSS_SUPPORT.md's unsupported
// table (and the guard above asserts that table stays present).
func TestWebCSSAcceptedPropertiesHaveCoverage(t *testing.T) {
	data, err := os.ReadFile("webpage.go")
	if err != nil {
		t.Fatalf("cascade source is missing: %v", err)
	}
	classified := map[string]bool{}
	for _, name := range []string{
		"align-items", "align-self", "aspect-ratio", "background", "background-color",
		"background-image", "background-size", "background-position", "background-repeat", "border", "border-bottom", "border-bottom-left-radius",
		"border-bottom-right-radius", "border-bottom-width", "border-color", "border-left",
		"border-left-width", "border-radius", "border-right", "border-right-width",
		"border-style", "border-top", "border-top-left-radius", "border-top-right-radius", "border-top-width",
		"border-width", "bottom", "box-shadow", "box-sizing", "clear", "color", "column-gap",
		"table-layout", "border-collapse", "border-spacing",
		"container", "container-name", "container-type", "content", "counter-increment",
		"counter-reset", "counter-set", "display", "flex",
		"flex-basis", "flex-direction", "flex-grow", "flex-shrink", "flex-wrap", "float", "font", "font-size",
		"font-style", "font-weight", "gap", "grid-template-columns", "height", "inset",
		"inset-block-start", "inset-block-end", "inset-inline-start", "inset-inline-end",
		"justify-content", "left", "letter-spacing", "line-height", "list-style", "list-style-type", "margin", "margin-block",
		"margin-block-end", "margin-block-start", "margin-bottom", "margin-inline",
		"margin-inline-end", "margin-inline-start", "margin-left", "margin-right", "margin-top",
		"max-height", "max-width", "min-height", "min-width", "object-fit", "object-position", "opacity", "order", "outline",
		"outline-color", "outline-offset", "outline-width", "padding", "padding-block", "padding-block-end",
		"padding-block-start", "padding-bottom", "padding-inline", "padding-inline-end",
		"padding-inline-start", "padding-left", "padding-right", "padding-top", "place-items",
		"position", "right", "row-gap", "text-align", "text-decoration", "text-decoration-color", "text-decoration-thickness",
		"text-decoration-line", "text-decoration-style", "text-indent", "text-shadow", "text-transform", "top",
		"overflow", "overflow-x", "overflow-y", "vertical-align", "visibility", "white-space", "width", "word-spacing", "z-index",
	} {
		classified[name] = true
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "webpage.go", data, 0)
	if err != nil {
		t.Fatalf("cascade source does not parse: %v", err)
	}
	addKey := func(lit *ast.BasicLit) {
		if lit == nil || lit.Kind != token.STRING {
			return
		}
		name := strings.Trim(lit.Value, "`\"")
		if !classified[name] {
			t.Fatalf("accepted CSS property %q is not classified as rendered or documented-unsupported", name)
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "applyWebDecls" && fn.Name.Name != "applyWebLayoutDecls") {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.IndexExpr:
				if id, ok := node.X.(*ast.Ident); ok && id.Name == "decls" {
					if lit, ok := node.Index.(*ast.BasicLit); ok {
						addKey(lit)
					}
				}
			case *ast.RangeStmt:
				if cl, ok := node.X.(*ast.CompositeLit); ok {
					for _, elt := range cl.Elts {
						switch e := elt.(type) {
						case *ast.BasicLit:
							addKey(e)
						case *ast.KeyValueExpr:
							if lit, ok := e.Key.(*ast.BasicLit); ok {
								addKey(lit)
							}
						case *ast.CompositeLit:
							for _, inner := range e.Elts {
								if lit, ok := inner.(*ast.BasicLit); ok {
									addKey(lit)
								}
							}
						}
					}
				}
			}
			return true
		})
		return false
	})
}

func TestWebCSSMaxWidthWrapsTextAndNarrowsBox(t *testing.T) {
	legibleTestInitFonts(t)
	text := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron"
	base := parseWebPage(`<style>p{background:#eef}</style><p>`+text+`</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	narrowDoc := parseWebPage(`<style>p{max-width:180px;background:#eef}</style><p>`+text+`</p>`, "", "https://example.com/")
	narrow := narrowDoc.computeLayout(&Config{}, Element{Font: "body"}, 800)
	countRows := func(layout *webLayout) int {
		n := 0
		for _, row := range layout.Rows {
			if len(row.Runs) > 0 {
				n++
			}
		}
		return n
	}
	baseRows, narrowRows := countRows(base), countRows(narrow)
	if narrowRows < baseRows+2 {
		t.Fatalf("max-width did not force extra wrapped lines: base %d rows, narrow %d rows", baseRows, narrowRows)
	}
	boxWidth := func(layout *webLayout) int32 {
		for _, rect := range layout.Rects {
			if rect.Color.R == 0xee && rect.Color.G == 0xee && rect.Color.B == 0xff && rect.Rect.W > 0 {
				return rect.Rect.W
			}
		}
		return 0
	}
	baseBox, narrowBox := boxWidth(base), boxWidth(narrow)
	if baseBox == 0 || narrowBox == 0 {
		t.Fatal("the max-width paragraph painted no background box")
	}
	if narrowBox*2 >= baseBox {
		t.Fatalf("max-width did not narrow the box: full=%d narrow=%d", baseBox, narrowBox)
	}
}

func TestWebCSSFloatDisplacesSiblingAndClearMovesItDown(t *testing.T) {
	legibleTestInitFonts(t)
	base := parseWebPage(`<div>Float</div><p>Following</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	floated := parseWebPage(`<style>.f{float:left;width:120px}</style><div class="f">Float</div><p>Following</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	_, baseX := webTestTextPosition(t, base, "Following")
	_, floatX := webTestTextPosition(t, floated, "Following")
	if floatX <= baseX+60 {
		t.Fatalf("float did not displace the following sibling: base x=%d, floated x=%d", baseX, floatX)
	}
	if len(floated.Floats) != 1 || len(floated.Floats[0].Rows) == 0 {
		t.Fatalf("float was not laid out as its own box: %+v", floated.Floats)
	}

	moving := `<style>.f{float:left;width:120px;min-height:160px}</style>` +
		`<div class="f">Float</div><p>Following</p><p class="after">After</p>`
	clearSource := `<style>.f{float:left;width:120px;min-height:160px}.after{clear:both}</style>` +
		`<div class="f">Float</div><p>Following</p><p class="after">After</p>`
	movingLayout := parseWebPage(moving, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	clearLayout := parseWebPage(clearSource, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	freeRow, _ := webTestTextPosition(t, movingLayout, "After")
	clearRow, _ := webTestTextPosition(t, clearLayout, "After")
	if clearRow < freeRow+4 {
		t.Fatalf("clear did not move the sibling below the float: free row %d, clear row %d", freeRow, clearRow)
	}
}

func TestWebCSSMinMaxSizeBoxSizingAndPercentage(t *testing.T) {
	legibleTestInitFonts(t)
	boxWidth := func(layout *webLayout) int32 {
		t.Helper()
		for _, rect := range layout.Rects {
			if rect.Color.R == 0xee && rect.Color.G == 0xee && rect.Color.B == 0xff && rect.Rect.W > 0 {
				return rect.Rect.W
			}
		}
		t.Fatal("no background box was painted")
		return 0
	}
	contentDoc := parseWebPage(`<style>p{width:240px;padding:20px;box-sizing:content-box;background:#eef}</style><p>Body</p>`, "", "https://example.com/")
	contentBox := contentDoc.computeLayout(&Config{}, Element{Font: "body"}, 800)
	borderBox := parseWebPage(`<style>p{width:240px;padding:20px;box-sizing:border-box;background:#eef}</style><p>Body</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	cw, bw := boxWidth(contentBox), boxWidth(borderBox)
	if cw-bw < 20 {
		t.Fatalf("box-sizing did not remove the horizontal padding from the width: content-box=%d border-box=%d", cw, bw)
	}

	percent := parseWebPage(`<style>p{width:50%;background:#eef}</style><p>Body</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	if pw := boxWidth(percent); pw <= 0 || pw >= boxWidth(parseWebPage(`<style>p{background:#eef}</style><p>Body</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)) {
		t.Fatalf("width:50%% did not narrow the box: %d", pw)
	}

	contradictory := parseWebPage(`<style>p{min-width:500px;max-width:100px;background:#eef}</style><p>Body</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	maxOnly := parseWebPage(`<style>p{max-width:100px;background:#eef}</style><p>Body</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	if got, small := boxWidth(contradictory), boxWidth(maxOnly); got <= small+100 {
		t.Fatalf("min-width did not win over a smaller max-width: min+max=%d max-only=%d", got, small)
	}
}

func TestWebCSSMinMaxHeightChangesRows(t *testing.T) {
	legibleTestInitFonts(t)
	maxLayout := parseWebPage(`<style>p{max-height:60px}</style><p>alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	maxRows := int32((60 + maxLayout.RowH - 1) / maxLayout.RowH)
	if int32(len(maxLayout.Rows)) > maxRows {
		t.Fatalf("max-height kept %d rows, want at most %d", len(maxLayout.Rows), maxRows)
	}
	minLayout := parseWebPage(`<style>p{min-height:120px}</style><p>Tiny</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	if height := int32(len(minLayout.Rows)) * minLayout.RowH; height < 120 {
		t.Fatalf("min-height produced only %d px", height)
	}
	heightLayout := parseWebPage(`<style>p{height:120px}</style><p>Tiny</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	if height := int32(len(heightLayout.Rows)) * heightLayout.RowH; height < 120 {
		t.Fatalf("height produced only %d px", height)
	}
}

func TestWebCSSRelativePositionOffsetsWithoutMovingFlow(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<p>Before</p><p class="move">Move</p><p>After</p>`
	base := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	rel := parseWebPage(`<style>.move{position:relative;left:24px;top:8px}</style>`+source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	baseRow, baseX := webTestTextPosition(t, base, "Move")
	relRow, relX := webTestTextPosition(t, rel, "Move")
	if relX <= baseX+10 {
		t.Fatalf("relative left did not move the run: base x=%d, moved x=%d", baseX, relX)
	}
	if rel.Rows[relRow].Y <= base.Rows[baseRow].Y+3 {
		t.Fatalf("relative top did not move the row: base y=%d, moved y=%d", base.Rows[baseRow].Y, rel.Rows[relRow].Y)
	}
	baseAfterRow, baseAfterX := webTestTextPosition(t, base, "After")
	relAfterRow, relAfterX := webTestTextPosition(t, rel, "After")
	if relAfterRow != baseAfterRow || relAfterX != baseAfterX {
		t.Fatalf("relative position changed the following flow: base (%d,%d), relative (%d,%d)", baseAfterRow, baseAfterX, relAfterRow, relAfterX)
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
		{"@supports (width: 10cqw)", true},
		{"@supports (width: calc(50cqw - 10px))", true},
		{"@supports (font-size: 2cqw)", true},
		{"@supports (width: potato-cqw)", false},
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
		{"@supports (display: inherit)", true},
		{"@supports (display: initial)", true},
		{"@supports (display: unset)", true},
		{"@supports (display: revert)", true},
		{"@supports (display: revert-layer)", true},
		{"@supports (all: inherit)", true},
		{"@supports (all: initial)", true},
		{"@supports (all: revert-layer)", true},
		{"@supports (all: none)", false},
		{"@supports (unknown-property: initial)", false},
		{"@supports (-webkit-border-radius: 4px)", true},
		{"@supports (-moz-border-radius: 4px)", true},
		{"@supports (-webkit-border-radius: inherit)", true},
		{"@supports (-webkit-border-radius: made-up)", false},
		{"@supports (-webkit-color: not-a-colour)", false},
		{"@supports (-webkit-unknown-property: 1)", false},
		{"@supports (-webkit-unknown-property: initial)", false},
		{"@supports (display: -webkit-flex)", true},
		{"@supports (display: -webkit-inline-flex)", true},
		{"@supports (display: -webkit-box)", true},
		{"@supports (display: -webkit-inline-box)", true},
		{"@supports (display: -webkit-grid)", false},
		{"@supports (display: -webkit-made-up)", false},
		{"@supports (display: -moz-flex)", false},
		{"@supports (display: -ms-flexbox)", false},
		{"@supports (position: -webkit-sticky)", false},
		{"@supports (position: -moz-sticky)", false},
		{"@supports (position: -webkit-absolute)", false},
		{"@supports (position: sticky)", true},
		{"@supports (background-image: -webkit-linear-gradient(red, blue))", true},
		{"@supports (background-image: -webkit-repeating-linear-gradient(red, blue))", true},
		{"@supports (background-image: -webkit-radial-gradient(red, blue))", true},
		{"@supports (background-image: -webkit-repeating-radial-gradient(red, blue))", true},
		{"@supports (background: -webkit-linear-gradient(red, blue))", true},
		{"@supports (background-image: -moz-linear-gradient(red, blue))", false},
		{"@supports (background-image: -webkit-conic-gradient(red, blue))", false},
		{"@supports (background-image: -webkit-linear-gradient(nope))", false},
		{"@supports (background-image: -webkit-image-set(url(a.png) 1x))", false},
		{"@supports (background-image: radial-gradient(red, blue))", true},
		{"@supports (background-image: conic-gradient(red, blue))", false},
		{"@supports (-webkit-background-clip: text)", true},
		{"@supports (background-clip: text)", true},
		{"@supports (-webkit-background-clip: made-up)", false},
		{"@supports (-webkit-background-clip: inherit)", true},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			if got := webSupportsApplies(tc.query); got != tc.want {
				t.Errorf("webSupportsApplies(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
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

func TestWebCSSSupportsKeepsVendorPrefixedBlock(t *testing.T) {
	source := `<style>` +
		`.notice{color:#111111}` +
		`@supports (-webkit-border-radius: 4px){.notice{color:#cc0000}}` +
		`@supports (-webkit-border-radius: made-up){.notice{color:#0000cc}}` +
		`@supports (display: -webkit-flex){.notice{color:#cc0000}}` +
		`@supports (display: -webkit-grid){.notice{color:#0000cc}}` +
		`</style><p class="notice">Visible notice</p>`
	doc := parseWebPage(source, "", "https://example.com/")
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if !strings.Contains(run.Text, "Visible notice") {
				continue
			}
			if !run.Style.HasColor || run.Style.Color.R != 0xcc || run.Style.Color.G != 0 || run.Style.Color.B != 0 {
				t.Fatalf("vendor-prefixed @supports block did not win: %+v", run.Style.Color)
			}
			return
		}
	}
	t.Fatal("the styled notice was not drawn")
}

func TestWebPrefixedDisplayValuesApply(t *testing.T) {
	for value, want := range map[string]int{
		"-webkit-flex":        webDisplayFlex,
		"-webkit-inline-flex": webDisplayFlex,
		"-webkit-box":         webDisplayFlex,
		"-webkit-inline-box":  webDisplayFlex,
	} {
		style := webBaseStyle()
		applyWebDecls(&style, map[string]string{"display": value})
		if style.Display != want {
			t.Errorf("display:%s produced %d, want %d", value, style.Display, want)
		}
	}
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

func TestWebContainerStyleQueries(t *testing.T) {
	cases := []struct {
		name  string
		query string
		html  string
		red   bool
	}{
		{
			name:  "inherited custom property hit",
			query: `@container style(--theme: dark){.card{color:#cc0000}}`,
			html:  `<div style="--theme:dark"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "inherited through intermediate element",
			query: `@container style(--theme: dark){.card{color:#cc0000}}`,
			html:  `<div style="--theme:dark"><section><p class="card">Card</p></section></div>`,
			red:   true,
		},
		{
			name:  "value miss",
			query: `@container style(--theme: dark){.card{color:#cc0000}}`,
			html:  `<div style="--theme:light"><p class="card">Card</p></div>`,
		},
		{
			name:  "nearest redeclaration wins",
			query: `@container style(--theme: dark){.card{color:#cc0000}}`,
			html:  `<div style="--theme:dark"><div style="--theme:light"><p class="card">Card</p></div></div>`,
		},
		{
			name:  "boolean property hit",
			query: `@container style(--compact){.card{color:#cc0000}}`,
			html:  `<div style="--compact:1"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "boolean property miss",
			query: `@container style(--compact){.card{color:#cc0000}}`,
			html:  `<div style="--theme:dark"><p class="card">Card</p></div>`,
		},
		{
			name:  "named style container hit",
			query: `@container sidebar style(--theme: dark){.card{color:#cc0000}}`,
			html:  `<div style="container-name:sidebar;--theme:dark"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "named style container miss",
			query: `@container sidebar style(--theme: dark){.card{color:#cc0000}}`,
			html:  `<div style="container-name:other;--theme:dark"><p class="card">Card</p></div>`,
		},
		{
			name:  "not style hit",
			query: `@container not style(--theme: dark){.card{color:#cc0000}}`,
			html:  `<div style="--theme:light"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "combined style and size hit",
			query: `@container style(--theme: dark) and (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;width:600px;--theme:dark"><p class="card">Card</p></div>`,
			red:   true,
		},
		{
			name:  "combined style and size miss",
			query: `@container style(--theme: dark) and (min-width:500px){.card{color:#cc0000}}`,
			html:  `<div style="container-type:inline-size;width:320px;--theme:dark"><p class="card">Card</p></div>`,
		},
		{
			name:  "custom property value is case sensitive",
			query: `@container style(--theme: dark){.card{color:#cc0000}}`,
			html:  `<div style="--theme:Dark"><p class="card">Card</p></div>`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := `<style>.card{color:#111111}` + tc.query + `</style>` + tc.html
			doc := parseWebPage(source, "", "https://example.com/")
			for _, block := range doc.Blocks {
				for _, run := range block.Runs {
					if !strings.Contains(run.Text, "Card") {
						continue
					}
					gotRed := run.Style.HasColor && run.Style.Color.R == 0xcc && run.Style.Color.G == 0 && run.Style.Color.B == 0
					if gotRed != tc.red {
						t.Fatalf("card color = %v, want red=%v (%+v)", run.Style.Color, tc.red, run.Style)
					}
					return
				}
			}
			t.Fatal("the card text was not drawn")
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
