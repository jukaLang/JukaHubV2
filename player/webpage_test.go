package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// --- CSS parsing ----------------------------------------------------------

func TestParseWebCSSRulesAndAtMedia(t *testing.T) {
	css := `
		/* comment { } */
		p { color: #123456; margin: 0 0 8px 0; }
		.note, #intro { font-weight: bold; }
		article .note { font-size: 1.5em; }
		@media (max-width: 700px) { .small { font-size: 12px; } }
		@font-face { font-family: X; src: url(x.woff); }
	`
	rules := parseWebCSS(css)
	if len(rules) != 5 {
		t.Fatalf("parsed %d rules, want 5: %+v", len(rules), rules)
	}
	bySelector := map[string]map[string]string{}
	for _, rule := range rules {
		bySelector[rule.selector] = rule.decls
	}
	if got := bySelector["p"]["color"]; got != "#123456" {
		t.Errorf("p colour = %q", got)
	}
	if got := bySelector[".note"]["font-weight"]; got != "bold" {
		t.Errorf(".note weight = %q", got)
	}
	if got := bySelector["article .note"]["font-size"]; got != "1.5em" {
		t.Errorf("descendant rule = %q", got)
	}
	if got := bySelector[".small"]["font-size"]; got != "12px" {
		t.Errorf("@media rule was dropped: %+v", bySelector)
	}

	if webSelectorSpecificity("#intro") <= webSelectorSpecificity(".note") {
		t.Error("id selector must outrank a class selector")
	}
	if webSelectorSpecificity(".note") <= webSelectorSpecificity("p") {
		t.Error("class selector must outrank a tag selector")
	}
}

func TestWebSelectorMatching(t *testing.T) {
	chain := []webElement{
		{tag: "html"},
		{tag: "body"},
		{tag: "article", classes: []string{"post", "wide"}, id: "main"},
		{tag: "p", classes: []string{"note"}},
	}
	cases := []struct {
		selector string
		want     bool
	}{
		{"p", true},
		{"p.note", true},
		{"p.missing", false},
		{"article p", true},
		{"body article p.note", true},
		{"aside p", false},
		{"#main p", true},
		{"div p", false},
		{"*", true},
		{"p:hover", true},
		{"p[data-x]", true},
	}
	for _, tc := range cases {
		if got := webSelectorMatches(tc.selector, chain); got != tc.want {
			t.Errorf("webSelectorMatches(%q) = %v, want %v", tc.selector, got, tc.want)
		}
	}
}

func TestParseWebColorAndLengths(t *testing.T) {
	cases := map[string]sdl.Color{
		"#fff":               {R: 255, G: 255, B: 255, A: 255},
		"#102030":            {R: 16, G: 32, B: 48, A: 255},
		"rgb(10, 20, 30)":    {R: 10, G: 20, B: 30, A: 255},
		"rgb(100%, 0%, 50%)": {R: 255, G: 0, B: 128, A: 255},
		"navy":               {R: 0, G: 0, B: 128, A: 255},
	}
	for input, want := range cases {
		got, ok := parseWebColor(input)
		if !ok {
			t.Errorf("parseWebColor(%q) failed", input)
			continue
		}
		if got != want {
			t.Errorf("parseWebColor(%q) = %+v, want %+v", input, got, want)
		}
	}
	if _, ok := parseWebColor("not-a-colour"); ok {
		t.Error("garbage colour accepted")
	}

	if got := webLength("12px", 16); got != 12 {
		t.Errorf("12px = %v", got)
	}
	if got := webLength("2em", 10); got != 20 {
		t.Errorf("2em at 10px = %v", got)
	}
	if got := webLength("150%", 20); got != 30 {
		t.Errorf("150%% of 20 = %v", got)
	}
	if px, ok := webFontSizePx("large", 16); !ok || px != 18 {
		t.Errorf("large = %v, %v", px, ok)
	}
}

// --- Document building ----------------------------------------------------

const webTestPage = `<html><head><title>Reader Test</title>
<style>
 body { background-color: #f4f5f8; color: #20242c; }
 h1 { color: #b02020; }
 p.lead { font-size: 20px; font-weight: bold; }
 .hidden { display: none; }
 article p { margin: 0 0 10px 0; }
</style>
</head><body>
<h1>Styled &amp; ready</h1>
<p class="lead">Lead paragraph with <a href="/more">a link</a>.</p>
<p>Body text that is <strong>bold</strong> and <em>slanted</em>.</p>
<ul><li>First item</li><li>Second item</li></ul>
<blockquote>Quoted text</blockquote>
<pre>code line 1
code line 2</pre>
<p class="hidden">invisible</p>
<script>var secret = "never render me";</script>
</body></html>`

func TestParseWebPageBuildsStyledBlocks(t *testing.T) {
	doc := parseWebPage(webTestPage, "", "https://example.com/reader")
	if doc.Title != "Reader Test" {
		t.Fatalf("title = %q", doc.Title)
	}
	if doc.Paper != (sdl.Color{R: 244, G: 245, B: 248, A: 255}) {
		t.Errorf("paper = %+v, want the body background", doc.Paper)
	}
	if doc.Text != (sdl.Color{R: 32, G: 36, B: 44, A: 255}) {
		t.Errorf("text = %+v, want the body colour", doc.Text)
	}
	if len(doc.Blocks) == 0 {
		t.Fatal("no blocks parsed")
	}

	var heading, lead, body *webBlock
	var items []string
	hiddenSeen, scriptSeen := false, false
	for i := range doc.Blocks {
		block := &doc.Blocks[i]
		text := ""
		for _, run := range block.Runs {
			text += run.Text
			if strings.Contains(run.Text, "never render me") {
				scriptSeen = true
			}
			if strings.Contains(run.Text, "invisible") {
				hiddenSeen = true
			}
		}
		switch {
		case strings.HasPrefix(text, "Styled"):
			heading = block
		case strings.HasPrefix(text, "Lead paragraph"):
			lead = block
		case strings.HasPrefix(text, "Body text"):
			body = block
		case block.Bullet != "":
			items = append(items, text)
		}
	}
	if heading == nil || !heading.Style.Bold || heading.Style.FontPx <= 16 {
		t.Fatalf("heading block = %+v", heading)
	}
	if heading.Style.Color != (sdl.Color{R: 176, G: 32, B: 32, A: 255}) {
		t.Errorf("css h1 colour not applied: %+v", heading.Style)
	}
	if lead == nil || lead.Style.FontPx != 20 || !lead.Style.Bold {
		t.Errorf("class rule not applied: %+v", lead)
	}
	if body == nil {
		t.Fatal("body paragraph missing")
	}
	boldFound, linkFound, quoteFound, preFound := false, false, false, false
	for _, run := range body.Runs {
		if run.Style.Bold && strings.Contains(run.Text, "bold") {
			boldFound = true
		}
	}
	for i := range doc.Blocks {
		for _, run := range doc.Blocks[i].Runs {
			if run.Style.Link && run.Style.Underline {
				linkFound = true
			}
		}
		if doc.Blocks[i].BorderLeft {
			quoteFound = true
		}
		if doc.Blocks[i].Pre && doc.Blocks[i].HasBg {
			preFound = true
		}
	}
	if !boldFound {
		t.Error("<strong> did not make the run bold")
	}
	if !linkFound {
		t.Error("<a> was not marked as an underlined link")
	}
	if !quoteFound {
		t.Error("blockquote lost its left rule")
	}
	if !preFound {
		t.Error("pre block lost its tinted background")
	}
	if len(items) != 2 || items[0] != "First item" || items[1] != "Second item" {
		t.Errorf("list items = %v", items)
	}
	if hiddenSeen {
		t.Error("display:none content was rendered")
	}
	if scriptSeen {
		t.Error("script source was rendered as text")
	}
}

// Decorations belong to the element that declares them even when the text is
// nested deeper: `<a><b>docs</b></a>` must stay link-coloured and underlined,
// and inline <code> must carry its own chip background.
func TestParseWebPageInheritsDecorations(t *testing.T) {
	doc := parseWebPage(`<body><p><a href="/docs"><b>docs</b></a> and <code>go test</code></p></body>`, "", "")
	if len(doc.Blocks) == 0 {
		t.Fatal("no blocks")
	}
	var boldRun, codeRun *webRun
	for i := range doc.Blocks {
		for j := range doc.Blocks[i].Runs {
			run := &doc.Blocks[i].Runs[j]
			if strings.Contains(run.Text, "docs") {
				boldRun = run
			}
			if strings.Contains(run.Text, "go") {
				codeRun = run
			}
		}
	}
	if boldRun == nil {
		t.Fatal("nested bold link run missing")
	}
	if !boldRun.Style.Bold || !boldRun.Style.Link || !boldRun.Style.Underline {
		t.Errorf("nested link lost bold/link/underline: %+v", boldRun.Style)
	}
	if codeRun == nil || !codeRun.Style.HasBg {
		t.Error("inline code lost its background")
	}
}

func TestParseWebPageInlineStyleWins(t *testing.T) {
	doc := parseWebPage(`<body><p style="color:#0055ff; font-size: 30px">Hi</p></body>`, `p { color: red; }`, "")
	if len(doc.Blocks) != 1 {
		t.Fatalf("blocks = %d", len(doc.Blocks))
	}
	style := doc.Blocks[0].Runs[0].Style
	if !style.HasColor || style.Color != (sdl.Color{R: 0, G: 85, B: 255, A: 255}) {
		t.Errorf("inline colour lost: %+v", style.Color)
	}
	if style.FontPx != 30 {
		t.Errorf("inline font-size = %v", style.FontPx)
	}
}

// A page that is only script/boilerplate has no readable document; the caller
// must be able to fall back to its plain-text extract.
func TestParseWebPageWithoutTextHasNoBlocks(t *testing.T) {
	doc := parseWebPage(`<html><head><script>var x = 1;</script></head><body></body></html>`, "", "")
	if len(doc.Blocks) != 0 {
		t.Fatalf("blocks = %+v, want none", doc.Blocks)
	}
}

func TestParseWebPageCapsHugeText(t *testing.T) {
	huge := strings.Repeat("word ", (webMaxTextBytes/5)+2000)
	doc := parseWebPage("<body><p>"+huge+"</p></body>", "", "")
	if len(doc.Blocks) == 0 {
		t.Fatal("no blocks")
	}
	last := doc.Blocks[len(doc.Blocks)-1]
	found := false
	for _, run := range last.Runs {
		if strings.Contains(run.Text, "truncated") {
			found = true
		}
	}
	if !found {
		t.Fatal("a page over the byte cap was not marked as truncated")
	}
}

// --- Layout and drawing ---------------------------------------------------

func layoutTestConfig(t *testing.T) *Config {
	t.Helper()
	// Fonts need SDL_ttf initialized; the probe frame does that once.
	withScreen(t, probeW, probeH)
	newProbeFrame(t)
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return config
}

func TestWebLayoutWrapsAndStyles(t *testing.T) {
	doc := parseWebPage(webTestPage, "", "https://example.com/reader")
	config := layoutTestConfig(t)
	elem := Element{Type: "textbrowser", Font: "small"}
	const width = 900

	layout := doc.layoutFor(config, elem, width)
	if layout == nil || len(layout.Rows) == 0 {
		t.Fatal("no layout produced")
	}
	if layout.RowH <= 0 {
		t.Fatalf("row height = %d", layout.RowH)
	}

	var bodyFontH, headingFontH int32
	paperSized := 0
	for i, row := range layout.Rows {
		if row.Y < 0 {
			t.Errorf("row %d has negative Y", i)
		}
		for _, run := range row.Runs {
			if run.Font == nil || run.Text == "" {
				t.Errorf("row %d has an empty run: %+v", i, run)
				continue
			}
			w, _, err := run.Font.SizeUTF8(run.Text)
			if err != nil {
				t.Errorf("measure %q: %v", run.Text, err)
				continue
			}
			if run.X < 0 || run.X+int32(w) > width {
				t.Errorf("row %d run %q at x=%d width=%d escapes %d", i, run.Text, run.X, w, width)
			}
			if run.Font.Height() > int(headingFontH) {
				headingFontH = int32(run.Font.Height())
			}
			if run.Font.Height() < int(headingFontH) && bodyFontH == 0 {
				bodyFontH = int32(run.Font.Height())
			}
			if row.Y+int32(run.Font.Height()) > layout.Height+layout.RowH {
				t.Errorf("row %d run %q overflows the layout", i, run.Text)
			}
			paperSized++
		}
	}
	if paperSized == 0 {
		t.Fatal("layout has no runs")
	}
	if headingFontH <= bodyFontH {
		t.Errorf("headings (%d) are not larger than body text (%d)", headingFontH, bodyFontH)
	}

	// Layout is cached and only recomputed when the width changes.
	again := doc.layoutFor(config, elem, width)
	if again != layout {
		t.Error("layout was recomputed for an unchanged page and width")
	}
	wider := doc.layoutFor(config, elem, width+200)
	if wider == layout {
		t.Error("a new width reused the old layout")
	}
}

func TestWebLayoutHonoursAlignmentAndBullets(t *testing.T) {
	doc := parseWebPage(`<body><h2 style="text-align:center">Centered</h2>
	<ul><li>One</li><li>Two</li></ul></body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	centered, bullets := false, 0
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text == "Centered" && run.X > 200 {
				centered = true
			}
			if run.Text == "•" {
				bullets++
			}
		}
	}
	if !centered {
		t.Error("centered heading was left-aligned")
	}
	if bullets != 2 {
		t.Errorf("rendered %d bullets, want 2", bullets)
	}
}

// Adjacent margins must collapse to the larger one. Adding them produced
// double-height holes between headings and paragraphs on real pages, which is
// most of a handheld screen's worth of wasted rows.
func TestWebLayoutCollapsesAdjacentMargins(t *testing.T) {
	doc := parseWebPage(`<body>
		<p style="margin: 0 0 60px 0">Alpha</p>
		<p style="margin: 60px 0 0 0">Beta</p>
		<p>Gamma</p>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 600)
	if layout == nil {
		t.Fatal("no layout")
	}
	rowOf := func(word string) int {
		for i, row := range layout.Rows {
			for _, run := range row.Runs {
				if strings.Contains(run.Text, word) {
					return i
				}
			}
		}
		return -1
	}
	alpha, beta, gamma := rowOf("Alpha"), rowOf("Beta"), rowOf("Gamma")
	if alpha < 0 || beta < 0 || gamma < 0 {
		t.Fatalf("missing rows: alpha=%d beta=%d gamma=%d", alpha, beta, gamma)
	}
	if gap := beta - alpha - 1; gap < 1 {
		t.Errorf("60px margins produced no gap (%d rows)", gap)
	} else if gap > 4 {
		t.Errorf("adjacent 60px margins produced %d blank rows; they were added instead of collapsed", gap)
	}
	// A margin on one side only still separates the paragraphs.
	if gap := gamma - beta - 1; gap < 1 {
		t.Errorf("paragraphs with default spacing produced no gap (%d rows)", gap)
	}
}

// The measure must be capped and centred: full-bleed rows on a 1280px panel
// are far too wide to read, and a column that keeps its margins looks like the
// page instead of a text dump.
func TestWebLayoutCapsAndCentresTheColumn(t *testing.T) {
	doc := parseWebPage(webTestPage, "", "https://example.com/reader")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 1280)
	if layout == nil {
		t.Fatal("no layout")
	}
	left, right := int32(1<<30), int32(0)
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			w, _, err := run.Font.SizeUTF8(run.Text)
			if err != nil {
				continue
			}
			if run.X < left {
				left = run.X
			}
			if end := run.X + int32(w); end > right {
				right = end
			}
		}
	}
	if left < 60 {
		t.Errorf("text starts at x=%d; the column is not centred", left)
	}
	if right-left > 1000 {
		t.Errorf("lines are %dpx wide; the measure was not capped", right-left)
	}
	if right > 1280-left+8 {
		t.Errorf("column is lopsided: %d..%d", left, right)
	}
}

// text-transform, text-indent and line-through are the three decorations that
// change how a paragraph reads, so they are measured and drawn for real.
func TestWebLayoutAppliesTransformIndentAndStrike(t *testing.T) {
	doc := parseWebPage(`<body>
		<p style="text-transform: uppercase">mix up</p>
		<p style="text-indent: 40px">indented start and a long tail that must wrap past the indent on following lines</p>
		<p>old price <s>was 20</s></p>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 320)
	if layout == nil {
		t.Fatal("no layout")
	}
	upper, strike := false, false
	start := -1
	for i, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text == "MIX" || run.Text == "UP" {
				upper = true
			}
			if strings.HasPrefix(run.Text, "indented") {
				start = i
			}
			if run.Strike {
				strike = true
			}
		}
	}
	if !upper {
		t.Error("text-transform: uppercase was not applied before measuring")
	}
	if !strike {
		t.Error("line-through did not reach the run")
	}
	if start < 0 {
		t.Fatal("indented paragraph did not lay out")
	}
	// The first line is pushed in, every following line of the same
	// paragraph returns to the left margin.
	firstX := layout.Rows[start].Runs[0].X
	laterX := firstX
	for i := start + 1; i < len(layout.Rows); i++ {
		if len(layout.Rows[i].Runs) == 0 {
			continue
		}
		line := ""
		for _, run := range layout.Rows[i].Runs {
			line += run.Text + " "
		}
		if strings.Contains(line, "old") || strings.Contains(line, "price") {
			break
		}
		laterX = layout.Rows[i].Runs[0].X
	}
	if firstX < laterX+20 {
		t.Errorf("first line x=%d is not indented past the rest x=%d", firstX, laterX)
	}
}

func TestWebLayoutDrawsBordersAndBackgroundShorthand(t *testing.T) {
	doc := parseWebPage(`<body>
		<div style="background: #102030 url(x.png) no-repeat; border: 1px solid #ff0000">card text</div>
		<h2 style="border-bottom: 1px solid #00ff00">Underlined heading</h2>
	</body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 800)
	if layout == nil {
		t.Fatal("no layout")
	}
	var cardBg, cardBorder, headingRule bool
	for _, rect := range layout.Rects {
		switch {
		case rect.Stroke && rect.Color == (sdl.Color{R: 255, G: 0, B: 0, A: 255}):
			cardBorder = true
		case !rect.Stroke && rect.Color == (sdl.Color{R: 16, G: 32, B: 48, A: 255}):
			cardBg = true
		case !rect.Stroke && rect.Color == (sdl.Color{R: 0, G: 255, B: 0, A: 255}) && rect.Rect.H <= 2:
			headingRule = true
		}
	}
	if !cardBg {
		t.Error("background shorthand colour was dropped")
	}
	if !cardBorder {
		t.Error("box border was not laid out as an outline")
	}
	if !headingRule {
		t.Error("border-bottom did not produce a rule under the heading")
	}
}

// A style boundary must not invent a gap: punctuation stays glued to the word
// it follows, while a real space in the source still separates words across a
// tag (`<a>GNU</a> (more ...`). Inserting a space at every run boundary was
// what produced "price , bold ," on real pages.
func TestWebLayoutKeepsPunctuationTight(t *testing.T) {
	doc := parseWebPage(`<body><p>Struck <s>old price</s>, <strong>bold</strong> and <a href="#">GNU</a> (more)</p></body>`, "", "")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 900)
	if layout == nil {
		t.Fatal("no layout")
	}
	type placed struct {
		text   string
		x, end int32
	}
	var runs []placed
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			w, _, _ := run.Font.SizeUTF8(run.Text)
			runs = append(runs, placed{run.Text, run.X, run.X + int32(w)})
		}
	}
	find := func(prefix string) int {
		for i, run := range runs {
			if strings.HasPrefix(run.text, prefix) {
				return i
			}
		}
		return -1
	}
	comma := find(",")
	if comma < 1 {
		t.Fatal("comma run not found")
	}
	if runs[comma].x != runs[comma-1].end {
		t.Errorf("comma starts at %d but the previous word ends at %d; a space was inserted", runs[comma].x, runs[comma-1].end)
	}
	paren := find("(")
	if paren < 1 {
		t.Fatal("paren run not found")
	}
	if runs[paren].x <= runs[paren-1].end {
		t.Errorf("paren starts at %d but the previous word ends at %d; the source space was lost", runs[paren].x, runs[paren-1].end)
	}
	and := find("and")
	if and < 1 || runs[and].x <= runs[and-1].end {
		t.Error("the word after an inline tag lost its separating space")
	}
}

func TestDrawWebDocumentPaintsStyledPage(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	doc := parseWebPage(webTestPage, "", "https://example.com/reader")
	config := layoutTestConfig(t)
	withScreen(t, probeW, probeH)
	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	elem := Element{Type: "textbrowser", Font: "small"}
	const width = 900
	layout := doc.layoutFor(config, elem, width)
	if layout == nil {
		t.Fatal("no layout")
	}

	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	rect := sdl.Rect{X: 10, Y: 10, W: width, H: 500}
	drawWebDocument(renderer, config, doc, layout, rect, 0)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}

	paper, textPixels, outside := 0, 0, 0
	for y := int32(0); y < probeH; y++ {
		for x := int32(0); x < probeW; x++ {
			i := (int(y)*int(probeW) + int(x)) * 4
			b, g, r := frame.buf[i], frame.buf[i+1], frame.buf[i+2]
			inside := x >= rect.X && x < rect.X+rect.W && y >= rect.Y && y < rect.Y+rect.H
			if !inside {
				if r|g|b != 0 {
					outside++
				}
				continue
			}
			if r == doc.Paper.R && g == doc.Paper.G && b == doc.Paper.B {
				paper++
			}
			if int(r)+int(g)+int(b) < 300 {
				textPixels++
			}
		}
	}
	if paper < 100000 {
		t.Errorf("page paper only covers %d pixels", paper)
	}
	if textPixels < 200 {
		t.Errorf("only %d dark text pixels were drawn", textPixels)
	}
	if outside != 0 {
		t.Errorf("%d pixels were painted outside the page rect", outside)
	}
}

// The browser must actually apply a linked stylesheet, not only <style> tags.
// The browser element must draw the styled document instead of the plain-text
// extract, and the page paper must cover its content area.
func TestRenderTextBrowserUsesStyledDocument(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config := layoutTestConfig(t)
	doc := parseWebPage(webTestPage, "", "https://example.com/reader")
	variable := "styled_test_content"
	setBrowserDocument(variable, doc)
	t.Cleanup(func() { setBrowserDocument(variable, nil) })

	frame := newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	elem := Element{
		Type: "textbrowser", Text: "web", Variable: variable, Font: "small",
		X: 24, Y: 72, Width: "1232", Height: "496",
	}
	renderer.SetDrawColor(0, 0, 0, 255)
	renderer.Clear()
	renderTextBrowser(renderer, config, elem)
	if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&frame.buf[0]), int(probeW)*4); err != nil {
		t.Skipf("ReadPixels unavailable: %v", err)
	}

	paper := 0
	for i := 0; i < len(frame.buf)/4; i++ {
		if frame.buf[i*4] == doc.Paper.B && frame.buf[i*4+1] == doc.Paper.G && frame.buf[i*4+2] == doc.Paper.R {
			paper++
		}
	}
	if paper < 100000 {
		t.Errorf("styled page paper covers only %d pixels of the browser panel", paper)
	}
}

// A styled page must scroll by rows: the second screenful differs from the
// first, and the last row can be the top row without drawing outside the rect.
func TestDrawWebDocumentScrollsByRow(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	doc := parseWebPage(webTestPage, "", "https://example.com")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 900)
	if layout == nil || len(layout.Rows) < 6 {
		t.Fatalf("layout too short: %+v", layout)
	}
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)

	capture := func(start int) []byte {
		renderer.SetDrawColor(0, 0, 0, 255)
		renderer.Clear()
		drawWebDocument(renderer, config, doc, layout, sdl.Rect{X: 10, Y: 10, W: 900, H: 300}, start)
		buf := make([]byte, probeW*probeH*4)
		if err := renderer.ReadPixels(nil, sdl.PIXELFORMAT_ARGB8888, unsafe.Pointer(&buf[0]), int(probeW)*4); err != nil {
			t.Skipf("ReadPixels unavailable: %v", err)
		}
		return buf
	}
	first := capture(0)
	second := capture(4)
	same := true
	for i := range first {
		if first[i] != second[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("scrolling to row 4 drew the same pixels")
	}
	_ = capture(len(layout.Rows) - 1)
}

func TestBrowseWebPageAppliesLinkedCSS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/style.css":
			w.Header().Set("Content-Type", "text/css")
			io.WriteString(w, `body { background-color: #101820; color: #f0f4f8; }
				h1 { color: #ff0000; }
				.lead { font-size: 26px; }`)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, `<html><head><title>Linked CSS</title>
				<link rel="stylesheet" href="/style.css">
				</head><body><h1>Headline</h1><p class="lead">Lead text</p></body></html>`)
		}
	}))
	defer server.Close()

	page := browseWebPageResult(server.URL)
	if page.Doc == nil {
		t.Fatal("no styled document parsed from the page")
	}
	if page.Doc.Paper != (sdl.Color{R: 16, G: 24, B: 32, A: 255}) {
		t.Errorf("paper = %+v, want the linked body background", page.Doc.Paper)
	}
	var headline *webStyle
	for i := range page.Doc.Blocks {
		for _, run := range page.Doc.Blocks[i].Runs {
			if strings.Contains(run.Text, "Headline") {
				style := run.Style
				headline = &style
			}
		}
	}
	if headline == nil || !headline.HasColor || headline.Color != (sdl.Color{R: 255, G: 0, B: 0, A: 255}) {
		t.Fatalf("linked h1 rule not applied: %+v", headline)
	}
	if !strings.Contains(page.Text, "Lead text") {
		t.Error("plain-text fallback lost the page text")
	}
}

func TestFetchWebStylesheetsBoundsAndResolves(t *testing.T) {
	var requested []string
	var requestedMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedMu.Lock()
		requested = append(requested, r.URL.Path)
		requestedMu.Unlock()
		w.Header().Set("Content-Type", "text/css")
		switch r.URL.Path {
		case "/a.css":
			io.WriteString(w, "p { color: #333; }")
		case "/b.css":
			io.WriteString(w, "h1 { background-color: #444; }")
		}
	}))
	defer server.Close()
	base, err := url.Parse(server.URL + "/docs/page")
	if err != nil {
		t.Fatal(err)
	}

	source := `<link rel="stylesheet" href="/a.css">` +
		`<link rel="stylesheet" href="/a.css">` +
		`<link rel="icon" href="/favicon.ico">` +
		`<link rel="stylesheet" href="/b.css">`
	css := fetchWebStylesheets(source, base)
	if len(requested) != 2 {
		t.Fatalf("requested %v, want only the two distinct stylesheets", requested)
	}
	if strings.Count(css, "color") != 2 {
		t.Errorf("stylesheets were not concatenated: %q", css)
	}
	// Sheets are fetched concurrently but must still cascade in document
	// order, so a later sheet can override an earlier one.
	if a, b := strings.Index(css, "#333"), strings.Index(css, "#444"); a < 0 || b < 0 || a > b {
		t.Errorf("stylesheets came back out of document order: %q", css)
	}
}

// Links on a styled page keep their targets, so the browser can walk them with
// the D-pad instead of guessing where an anchor points. In-page anchors and
// non-web schemes are not pages that can be opened and never get a cursor stop.
func TestWebLayoutCollectsFollowableLinks(t *testing.T) {
	doc := parseWebPage(`<body>
		<p>Read the <a href="/docs/start">quick start</a> and the
		<a href="https://example.com/issues?x=1&amp;y=2"><b>issue list</b></a>.</p>
		<p><a href="#comments">jump</a> <a href="javascript:void(0)">js</a>
		<a href="mailto:hi@example.com">mail</a> <a href="/docs/a:b">path colon</a></p>
	</body>`, "", "https://example.com/reader")
	config := layoutTestConfig(t)
	layout := doc.layoutFor(config, Element{Type: "textbrowser", Font: "small"}, 900)
	if layout == nil {
		t.Fatal("no layout")
	}
	if len(layout.Links) != 3 {
		t.Fatalf("links = %+v, want the two web links and the path with a colon", layout.Links)
	}
	if layout.Links[0].Href != "/docs/start" || layout.Links[0].Text != "quick start" {
		t.Errorf("first link = %+v", layout.Links[0])
	}
	if layout.Links[1].Href != "https://example.com/issues?x=1&y=2" {
		t.Errorf("entity-encoded href = %q", layout.Links[1].Href)
	}
	if layout.Links[2].Href != "/docs/a:b" {
		t.Errorf("a path containing a colon was read as a scheme: %+v", layout.Links[2])
	}
	for i, hit := range layout.Links {
		if hit.Row < 0 || hit.Row >= len(layout.Rows) {
			t.Errorf("link %d sits on row %d of %d", i, hit.Row, len(layout.Rows))
		}
		if hit.W <= 0 {
			t.Errorf("link %d has no width: %+v", i, hit)
		}
	}
}

func TestWebHrefFollowable(t *testing.T) {
	cases := map[string]bool{
		"/docs/start":         true,
		"https://example.com": true, "HTTP://example.com": true,
		"#comments":               false,
		"":                        false,
		"javascript:void(0)":      false,
		"JaVaScRiPt:alert(1)":     false,
		"mailto:hi@example.com":   false,
		"data:text/html,<b>x</b>": false,
		"file:///etc/passwd":      false,
		"/docs/a:b":               true,
	}
	for href, want := range cases {
		if got := webHrefFollowable(href); got != want {
			t.Errorf("webHrefFollowable(%q) = %v, want %v", href, got, want)
		}
	}
}
