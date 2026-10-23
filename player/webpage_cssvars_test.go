package main

// Tests for the modern-CSS value layer of the reader: custom properties,
// var() with fallbacks, !important ordering and calc(). They check the styles
// that reach a block, not the parser's internals, so a block that renders is
// what proves the value resolved.

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// webTestBlock finds the block whose text starts with prefix.
func webTestBlock(t *testing.T, doc *webDocument, prefix string) *webBlock {
	t.Helper()
	for i := range doc.Blocks {
		text := ""
		for _, run := range doc.Blocks[i].Runs {
			text += run.Text
		}
		if strings.HasPrefix(strings.TrimSpace(text), prefix) {
			return &doc.Blocks[i]
		}
	}
	t.Fatalf("no block starting with %q in %d blocks", prefix, len(doc.Blocks))
	return nil
}

// A `--name` declared on an ancestor is visible to every descendant, and a
// var() reference resolves to the colour or length it names.
func TestWebCustomPropertiesResolveThroughTheCascade(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
		:root { --ink: #b02020; }
		html { --pad: 12px; }
		h1 { color: var(--ink); }
		body { --pad: 18px; }
		p.card { padding-left: var(--pad); }
	</style></head><body>
		<h1>Headline</h1>
		<p class="card">Card text</p>
	</body></html>`, "", "https://example.com/vars")

	heading := webTestBlock(t, doc, "Headline")
	if !heading.Style.HasColor || heading.Style.Color != (sdl.Color{R: 176, G: 32, B: 32, A: 255}) {
		t.Fatalf("h1 colour = %+v, want the var(--ink) red", heading.Style.Color)
	}
	// body redefines --pad, so the later declaration wins for its children.
	card := webTestBlock(t, doc, "Card text")
	if card.Indent != 18 {
		t.Fatalf("card indent = %v, want 18px from the inherited --pad", card.Indent)
	}
}

// A reference with no matching property falls back to the second argument, and
// a definition that only refers to itself must not spin: both are what a sheet
// expects from a browser.
func TestWebVarFallbackAndCycle(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
		:root { --loop-a: var(--loop-b); --loop-b: var(--loop-a); }
		p.missing { color: var(--never-defined, #123456); }
		p.cycle { color: var(--loop-a, #00ff00); }
		p.undefined { font-size: var(--nowhere); }
	</style></head><body>
		<p class="missing">Fallback text</p>
		<p class="cycle">Cycle text</p>
		<p class="undefined">Undefined text</p>
	</body></html>`, "", "")

	fallback := webTestBlock(t, doc, "Fallback text")
	if fallback.Style.Color != (sdl.Color{R: 0x12, G: 0x34, B: 0x56, A: 255}) {
		t.Fatalf("fallback colour = %+v, want #123456", fallback.Style.Color)
	}
	cycle := webTestBlock(t, doc, "Cycle text")
	if cycle.Style.Color != (sdl.Color{R: 0, G: 255, B: 0, A: 255}) {
		t.Fatalf("cyclic custom property did not fall back: %+v", cycle.Style.Color)
	}
	// An unresolvable var() invalidates the declaration: the paragraph keeps
	// the inherited size instead of becoming 0px.
	undefined := webTestBlock(t, doc, "Undefined text")
	if undefined.Style.FontPx != 16 {
		t.Fatalf("undefined var() left font size %v, want the inherited 16", undefined.Style.FontPx)
	}
}

// !important wins over a more specific selector, in either letter case, while
// a normal declaration from that selector still wins over a normal one.
func TestWebImportantOutranksSpecificity(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
		#main { color: #0000ff; }
		.note { color: #ff0000 !important; }
		.other { color: #00aa00; }
		.tone { color: #333333; font-weight: bold; }
		#main.tone { color: #990000; font-weight: normal; }
	</style></head><body>
		<p id="main" class="note">Important text</p>
		<p id="main" class="tone">Normal cascade text</p>
	</body></html>`, "", "")

	important := webTestBlock(t, doc, "Important text")
	if important.Style.Color != (sdl.Color{R: 255, G: 0, B: 0, A: 255}) {
		t.Fatalf("!important colour = %+v, want it to beat the #main rule", important.Style.Color)
	}
	// Without !important the id+class rule still outranks the class alone.
	normal := webTestBlock(t, doc, "Normal cascade text")
	if normal.Style.Color != (sdl.Color{R: 153, G: 0, B: 0, A: 255}) {
		t.Fatalf("normal cascade colour = %+v, want the #main.tone value", normal.Style.Color)
	}
	if normal.Style.Bold {
		t.Error("#main.tone cleared font-weight, but the block is still bold")
	}
}

// calc() reduces to a plain px length when every operand is absolute, and is
// left alone when a percentage is involved because the layout has not decided
// the reference width yet.
func TestWebCalcReducesAbsoluteLengths(t *testing.T) {
	cases := map[string]string{
		"calc(10px + 1rem)":     "26px",
		"calc(2 * 4px)":         "8px",
		"calc((4px + 2px) * 2)": "12px",
		"calc(20px / 4)":        "5px",
		"calc(-6px + 10px)":     "4px",
		"10px calc(2em + 2px)":  "10px 42px",
		"calc(100% - 20px)":     "calc(100% - 20px)",
		"calc(100vh - 2rem)":    "calc(100vh - 2rem)",
	}
	for input, want := range cases {
		if got := webSubstituteCalc(input, 20); got != want {
			t.Errorf("webSubstituteCalc(%q) = %q, want %q", input, got, want)
		}
	}

	// The reduced text has to reach the length parser as a normal length.
	if got := webLength(webSubstituteCalc("calc(10px + 1rem)", 16), 16); got != 26 {
		t.Errorf("calc length = %v, want 26", got)
	}

	doc := parseWebPage(`<html><head><style>
		h1 { margin-top: calc(8px + 2px); font-size: calc(1rem + 6px); }
	</style></head><body><h1>Calc heading</h1></body></html>`, "", "")
	heading := webTestBlock(t, doc, "Calc heading")
	if heading.MarginTop != 10 {
		t.Errorf("heading margin-top = %v, want the calc() 10px", heading.MarginTop)
	}
	if heading.Style.FontPx != 22 {
		t.Errorf("heading font size = %v, want calc(1rem + 6px) = 22", heading.Style.FontPx)
	}
}

// A var() in a style attribute resolves against the properties the element
// already inherits, which is how a page overrides one colour inline.
func TestWebVarInInlineStyle(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
		body { --accent: #0088cc; }
	</style></head><body>
		<p style="color: var(--accent)">Inline accent</p>
	</body></html>`, "", "")
	block := webTestBlock(t, doc, "Inline accent")
	if block.Style.Color != (sdl.Color{R: 0, G: 136, B: 204, A: 255}) {
		t.Fatalf("inline var() colour = %+v, want the body's --accent", block.Style.Color)
	}
}

// The `!important` flag has to be stripped before the value is parsed: a colour
// followed by the flag is not a colour.
func TestWebDropImportant(t *testing.T) {
	cases := []struct {
		in        string
		want      string
		important bool
	}{
		{"#fff", "#fff", false},
		{"#fff !important", "#fff", true},
		{"#fff!IMPORTANT", "#fff", true},
		{" #fff ! important ", " #fff ! important ", false},
		{"red", "red", false},
	}
	for _, tc := range cases {
		got, important := webDropImportant(tc.in)
		if got != tc.want || important != tc.important {
			t.Errorf("webDropImportant(%q) = %q, %v; want %q, %v", tc.in, got, important, tc.want, tc.important)
		}
	}
}

// Custom properties must not become blocks or text of their own, and a page
// that leans on them still renders its paper and body colour.
func TestWebCustomPropertyDefinitionsStayInvisible(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
		:root { --bg: #101820; --fg: #f0f4f8; }
		body { background-color: var(--bg); color: var(--fg); }
	</style></head><body><p>Themed body</p></body></html>`, "", "")
	if doc.Paper != (sdl.Color{R: 16, G: 24, B: 32, A: 255}) {
		t.Errorf("paper = %+v, want the var(--bg) colour", doc.Paper)
	}
	if doc.Text != (sdl.Color{R: 240, G: 244, B: 248, A: 255}) {
		t.Errorf("text = %+v, want the var(--fg) colour", doc.Text)
	}
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if strings.Contains(run.Text, "--bg") || strings.Contains(run.Text, "var(") {
				t.Errorf("custom property leaked into the page text: %q", run.Text)
			}
		}
	}
}
