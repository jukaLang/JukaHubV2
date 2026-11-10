package main

// Tests for the reader's CSS selector matcher (webpage.go). It used to assume
// that every attribute selector and every pseudo-class or pseudo-element held,
// so a reset sheet's `[hidden] { display: none }` matched every element and the
// page drew blank. Each test states the rule it pins down: attribute selectors
// and the pseudo-classes a static page can answer are evaluated, a
// pseudo-element rule never styles the element, a rule that would hide what it
// matches applies only on a definite match, and other rules keep the old
// leniency for what the streaming parser cannot decide.

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

const cssmURL = "https://example.com/page"

var (
	cssmGreen = sdl.Color{G: 255, A: 255}
	cssmRed   = sdl.Color{R: 255, A: 255}
	cssmBlue  = sdl.Color{B: 255, A: 255}
)

// cssmText joins the runs the page would draw, one block per line.
func cssmText(doc *webDocument) string {
	var b strings.Builder
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			b.WriteString(run.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// cssmRun returns the run that draws text.
func cssmRun(t *testing.T, doc *webDocument, text string) webRun {
	t.Helper()
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if strings.Contains(run.Text, text) {
				return run
			}
		}
	}
	t.Fatalf("no run draws %q; the page drew:\n%s", text, cssmText(doc))
	return webRun{}
}

// cssmExpect checks which texts the page draws and which it hides.
func cssmExpect(t *testing.T, doc *webDocument, present, absent []string) {
	t.Helper()
	text := cssmText(doc)
	for _, want := range present {
		if !strings.Contains(text, want) {
			t.Errorf("%q is missing; the page drew:\n%s", want, text)
		}
	}
	for _, gone := range absent {
		if strings.Contains(text, gone) {
			t.Errorf("%q was drawn although its rule hides it", gone)
		}
	}
}

// cssmChain builds the chain of open elements a start tag sees, outermost
// first. Each entry is a tag name, optionally followed by its attribute text.
func cssmChain(tags ...string) []webElement {
	chain := make([]webElement, 0, len(tags))
	for _, tag := range tags {
		name, attrs := tag, ""
		if i := strings.IndexByte(tag, ' '); i >= 0 {
			name, attrs = tag[:i], tag[i:]
		}
		chain = append(chain, webNewElement(name, attrs))
	}
	return chain
}

// cssmMatch compiles a selector and matches it against the chain's last element.
func cssmMatch(selector string, chain []webElement, hiding bool) bool {
	sel, _ := webCompileSelector(selector)
	return sel.matches(chain, hiding)
}

// The cloak and hidden attributes hide the element that carries them and
// nothing else. Every one of these rules used to match every element.
func TestWebCSSAttributeRulesHideOnlyTheirElements(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
[hidden] { display: none !important; }
[v-cloak] { display: none; }
[x-cloak] { display: none !important; }
[aria-hidden="true"] { display: none; }
</style></head><body>
<h1>Headline stays</h1>
<p>Paragraph stays</p>
<div id="app" v-cloak><p>Vue template text</p></div>
<div x-cloak>Alpine template text</div>
<p hidden>Hidden attribute text</p>
<span aria-hidden="true">menu_icon</span>
<p>Tail stays</p>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc,
		[]string{"Headline stays", "Paragraph stays", "Tail stays"},
		[]string{"Vue template text", "Alpine template text", "Hidden attribute text", "menu_icon"})
}

// A page built on Bootstrap's reboot draws its text: `[hidden]` hides only the
// parked dialog, `:not(.show)` and the utility classes hide what they name, a
// later `.show` rule reveals its menu, and the reboot's ::before/::after and
// :hover rules style nothing.
func TestWebCSSBootstrapRebootKeepsThePage(t *testing.T) {
	doc := parseWebPage(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Docs</title>
<style>
*,::after,::before{box-sizing:border-box}
body{margin:0;font-family:system-ui;line-height:1.5;color:#212529;background-color:#fff}
[hidden]{display:none!important}
[role=button]{cursor:pointer}
[type=button]:not(:disabled),[type=reset]:not(:disabled),button:not(:disabled){cursor:pointer}
::-moz-focus-inner{padding:0;border-style:none}
.visually-hidden-focusable:not(:focus):not(:focus-within){position:absolute!important}
.d-none{display:none!important}
.collapse:not(.show){display:none}
.dropdown-menu{display:none}
.dropdown-menu.show{display:block}
.btn:hover{color:#ff0000}
</style>
</head>
<body>
<nav class="navbar"><a class="navbar-brand" href="/">Brand name</a>
<div class="collapse navbar-collapse" id="nav">Collapsed links</div></nav>
<div class="container">
<h1>Getting started</h1>
<p class="lead">Bootstrap pages keep their text.</p>
<button type="button" class="btn">Click me</button>
<div class="dropdown-menu show">Open menu</div>
<div class="dropdown-menu">Closed menu</div>
<div class="d-none">Utility hidden</div>
<p hidden>Parked dialog</p>
</div>
</body>
</html>`, "", cssmURL)
	cssmExpect(t, doc,
		[]string{"Brand name", "Getting started", "Bootstrap pages keep their text.", "Click me", "Open menu"},
		[]string{"Collapsed links", "Closed menu", "Utility hidden", "Parked dialog"})
	if run := cssmRun(t, doc, "Click me"); run.Style.HasColor && run.Style.Color == cssmRed {
		t.Error(".btn:hover coloured a button nobody is pointing at")
	}
}

// A pseudo-element rule styles a generated box, never the element: hiding an
// icon's ::before must not hide the icon's label.
func TestWebCSSPseudoElementRulesNeverStyleTheElement(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
.icon::before { display: none; }
.note:before { display: none; }
.lead::after { visibility: hidden; }
p::first-line { color: #ff0000; }
p::marker, p::selection { display: none; }
input::placeholder { display: none; }
.x::-webkit-scrollbar { display: none; }
</style></head><body>
<p class="icon">Icon label</p>
<p class="note">Note text</p>
<p class="lead">Lead text</p>
<p class="x">Scroller text</p>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Icon label", "Note text", "Lead text", "Scroller text"}, nil)
	if run := cssmRun(t, doc, "Lead text"); run.Style.HasColor && run.Style.Color == cssmRed {
		t.Error("p::first-line coloured the whole paragraph")
	}

	chain := cssmChain("html", "body", `p class="icon"`)
	for _, selector := range []string{
		"p::before", "p:before", "p::after", "p:after", "p:first-line", "p::first-letter",
		"p::marker", "::selection", "p::placeholder", "p::backdrop", "p::file-selector-button",
		"p::part(label)", ".icon::-webkit-scrollbar", "p:hover::before", "body .icon::after",
	} {
		sel, _ := webCompileSelector(selector)
		if !sel.never {
			t.Errorf("%q was not recognised as a pseudo-element selector", selector)
		}
		if cssmMatch(selector, chain, false) || cssmMatch(selector, chain, true) {
			t.Errorf("%q matched the element itself", selector)
		}
	}

	// Rules for generated boxes never reach the matching loop at all.
	rules := parseWebCSS(`.a::before { color: red } .b { color: blue } .c:after, .d { color: green }`)
	var kept []string
	for _, rule := range rules {
		kept = append(kept, rule.selector)
	}
	if strings.Join(kept, " | ") != ".b | .d" {
		t.Errorf("rules kept = %q, want .b and .d", kept)
	}
}

// User-action and playback states are false on a page nobody is pointing at,
// focusing or playing; :not() of one of them is therefore true.
func TestWebCSSUserActionPseudoClassesNeverHold(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
p.c { color: #00ff00; }
p.c:hover { color: #ff0000; }
.menu:focus-within { display: none; }
a:visited { display: none; }
.card:hover .card-body { visibility: hidden; }
</style></head><body>
<p class="c">Hover text</p>
<div class="menu"><p>Menu text</p></div>
<p><a href="/x">Visited link</a></p>
<div class="card"><div class="card-body">Card body</div></div>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Hover text", "Menu text", "Visited link", "Card body"}, nil)
	if run := cssmRun(t, doc, "Hover text"); !run.Style.HasColor || run.Style.Color != cssmGreen {
		t.Errorf("p.c:hover overrode p.c: colour %+v (set %t)", run.Style.Color, run.Style.HasColor)
	}

	chain := cssmChain("html", "body", `a href="/x"`)
	for _, selector := range []string{
		":hover", ":active", ":focus", ":focus-visible", ":focus-within", ":visited", ":target",
		":target-within", ":fullscreen", ":modal", ":popover-open", ":user-invalid", ":autofill",
		":playing", ":paused", ":-webkit-autofill", ":-moz-focusring", "body:hover a",
	} {
		if cssmMatch(selector, chain, false) || cssmMatch(selector, chain, true) {
			t.Errorf("%q matched a page at rest", selector)
		}
	}
	for _, selector := range []string{":not(:hover)", "a:not(:focus):not(:visited)"} {
		if !cssmMatch(selector, chain, false) || !cssmMatch(selector, chain, true) {
			t.Errorf("%q did not match a page at rest", selector)
		}
	}
}

// Every attribute operator, quoting style and flag, with HTML's
// case-insensitive attribute names and values decoded like htmlAttrValue.
func TestWebCSSAttributeOperators(t *testing.T) {
	chain := cssmChain(
		`html lang="en-US"`,
		`body class="page dark-mode" data-theme="Dark"`,
		`a href="https://example.com/files/report.pdf" title="a &amp; b" data-empty="" data-br="x]y" rel="NoFollow"`,
	)
	cases := []struct {
		selector string
		want     bool
	}{
		{"[href]", true},
		{"[HREF]", true}, // attribute names are case-insensitive in HTML
		{"[download]", false},
		{`[href="https://example.com/files/report.pdf"]`, true},
		{`[href='https://example.com/files/report.pdf']`, true},
		{`[href="https://example.com/files/"]`, false},
		{"[href^=https]", true},
		{`[href^="ftp"]`, false},
		{`[href$=".pdf"]`, true},
		{`[href$=".PDF"]`, false},
		{`[href$=".PDF" i]`, true},
		{`[href*="/files/"]`, true},
		{`[href*="/images/"]`, false},
		{`[href^=""]`, false}, // an empty ^=, $= or *= value matches nothing
		{`[href$=""]`, false},
		{`[href*=""]`, false},
		{"[data-empty]", true},
		{`[data-empty=""]`, true},
		{`[title="a & b"]`, true}, // character references are decoded
		{`[title="a \26  b"]`, true},
		{`[data-br="x]y"]`, true}, // a ']' inside quotes does not end the selector
		{`[data-br=x\]y]`, true},
		{`a[data-br="x]"]`, false},
		{`[rel="nofollow"]`, true}, // rel is one of HTML's case-insensitive values
		{`[rel="nofollow" s]`, false},
		{`a[ href ^= "https" ]`, true},
		{"body[class~=dark-mode] a", true},
		{"body[class~=dark] a", false},
		{`body[class~=""] a`, false},
		{`[data-theme="dark"] a`, false},
		{`[data-theme="dark" i] a`, true},
		{"html[lang|=en] a", true},
		{"html[lang|=e] a", false},
		{"[lang|=en]", false}, // the <a> itself has no lang
		{"a:not([download])", true},
		{"a:not([href])", false},
	}
	for _, tc := range cases {
		for _, hiding := range []bool{false, true} {
			if got := cssmMatch(tc.selector, chain, hiding); got != tc.want {
				t.Errorf("%q (hiding=%t) = %t, want %t", tc.selector, hiding, got, tc.want)
			}
		}
	}
}

// `>` is evaluated against the real parent for a rule that hides; any other
// rule keeps reading it as a descendant combinator.
func TestWebCSSChildCombinator(t *testing.T) {
	chain := cssmChain("html", "body", `div class="menu"`, "div", `p class="item"`)
	cases := []struct {
		selector       string
		exact, lenient bool
	}{
		{".menu > .item", false, true},
		{"div > .item", true, true},
		{".menu > div > .item", true, true},
		{"body > div .item", true, true}, // the first div that fits is not the right one
		{".menu div > p.item", true, true},
		{"html > body > div > div > p", true, true},
		{"body > .item", false, true},
		{".menu > div > div > .item", false, false},
		{".menu .item", true, true},
	}
	for _, tc := range cases {
		if got := cssmMatch(tc.selector, chain, true); got != tc.exact {
			t.Errorf("%q for a hiding rule = %t, want %t", tc.selector, got, tc.exact)
		}
		if got := cssmMatch(tc.selector, chain, false); got != tc.lenient {
			t.Errorf("%q for another rule = %t, want %t", tc.selector, got, tc.lenient)
		}
	}

	doc := parseWebPage(`<html><head><style>.menu > .item { display: none; }</style></head><body>
<div class="menu"><p class="item">Direct child</p><div><p class="item">Grandchild item</p></div></div>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Grandchild item"}, []string{"Direct child"})
}

// The previous sibling is gone by the time a streaming parser styles an
// element, so a hiding rule never matches through `+` or `~`.
func TestWebCSSSiblingCombinatorsNeverHide(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
h2 + p { display: none; }
h2 ~ .more { display: none; }
.toggle:not(:checked) ~ .panel { display: none; }
</style></head><body>
<h2>Title</h2><p>Right after the heading</p><div class="more">More text</div>
<input type="checkbox" class="toggle"><div class="panel">Panel text</div>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Right after the heading", "More text", "Panel text"}, nil)

	chain := cssmChain("html", "body", "div", `p class="item"`)
	if cssmMatch("div ~ .item", chain, true) || cssmMatch("div + .item", chain, true) {
		t.Error("a hiding rule matched through a sibling combinator")
	}
	// Other rules keep the documented descendant reading.
	if !cssmMatch("div ~ .item", chain, false) {
		t.Error("div ~ .item no longer reads as a descendant selector for other rules")
	}
}

// Attribute selectors and pseudo-classes count as classes, pseudo-elements as
// types, :where() as nothing and :is()/:not()/:has() as their most specific
// argument.
func TestWebCSSSpecificity(t *testing.T) {
	spec := func(selector string) int {
		_, n := webCompileSelector(selector)
		return n
	}
	ordered := []string{"*", "div", "div p", "[data-x]", "div[data-x]", ".a:hover", "#a"}
	for i := 1; i < len(ordered); i++ {
		if spec(ordered[i-1]) >= spec(ordered[i]) {
			t.Errorf("specificity(%q) = %d is not below specificity(%q) = %d",
				ordered[i-1], spec(ordered[i-1]), ordered[i], spec(ordered[i]))
		}
	}
	for _, pair := range [][2]string{
		{"[data-x]", ".a"},
		{":hover", ".a"},
		{":first-child", ".a"},
		{"p::before", "div p"},
		{":is(#a, .b)", "#a"},
		{":not(.a, .b.c)", ".b.c"},
		{":has(> img.x)", "img.x"},
		{"div:where(.x)", "div"},
		{":where(#a .b)", "*"},
	} {
		if spec(pair[0]) != spec(pair[1]) {
			t.Errorf("specificity(%q) = %d, want specificity(%q) = %d", pair[0], spec(pair[0]), pair[1], spec(pair[1]))
		}
	}
	if spec(".a .b .c .d .e .f .g .h .i .j .k") >= spec("#a") {
		t.Error("eleven classes outranked an id")
	}

	// The cascade orders by that specificity before source order.
	doc := parseWebPage(`<html><head><style>
[data-x] { color: #00ff00; }
div { color: #ff0000; }
</style></head><body><div data-x>Attribute wins</div></body></html>`, "", cssmURL)
	if run := cssmRun(t, doc, "Attribute wins"); !run.Style.HasColor || run.Style.Color != cssmGreen {
		t.Errorf("[data-x] lost to a later div rule: colour %+v", run.Style.Color)
	}
}

// What a start tag cannot decide (:first-child, :nth-child(), :empty, :has(),
// a custom element's :defined) never hides anything, and still applies a
// harmless rule the way the reader always has.
func TestWebCSSUndecidableTestsOnlyApplyWhenHarmless(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
li:first-child { display: none; }
li:nth-child(2) { font-weight: bold; }
p:empty { display: none; }
.box:has(img) { visibility: hidden; }
:not(:defined) { visibility: hidden; }
</style></head><body>
<ul><li>First item</li><li>Second item</li></ul>
<p>Not empty</p>
<div class="box">Box text</div>
<my-widget>Widget text</my-widget>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"First item", "Second item", "Not empty", "Box text", "Widget text"}, nil)
	if run := cssmRun(t, doc, "Second item"); !run.Style.Bold {
		t.Error("li:nth-child(2) { font-weight: bold } no longer applies")
	}
}

// The pseudo-classes a start tag can answer from the element's own attributes
// and its ancestors. exact is the answer for a rule that would hide the
// element, lenient the answer for any other rule; they differ only where the
// test cannot be decided.
func TestWebCSSStaticPseudoClasses(t *testing.T) {
	cases := []struct {
		chain          []string
		selector       string
		exact, lenient bool
	}{
		{[]string{`input type="checkbox" checked`}, ":checked", true, true},
		{[]string{`input type="RADIO" checked`}, ":checked", true, true},
		{[]string{`input type="checkbox"`}, ":checked", false, false},
		{[]string{`input type="text" checked`}, ":checked", false, false},
		{[]string{`option selected`}, ":checked", true, true},
		{[]string{"option"}, ":checked", false, true},
		{[]string{"input disabled"}, ":disabled", true, true},
		{[]string{"input disabled"}, ":enabled", false, false},
		{[]string{"input"}, ":enabled", true, true},
		{[]string{"fieldset disabled", "input"}, "input:disabled", true, true},
		{[]string{"fieldset disabled", "legend", "input"}, ":disabled", false, true},
		{[]string{"optgroup disabled", "option"}, ":disabled", true, true},
		{[]string{"div"}, ":disabled", false, false},
		{[]string{"div"}, ":enabled", false, false},
		{[]string{"input required"}, ":required", true, true},
		{[]string{"input required"}, ":optional", false, false},
		{[]string{"textarea"}, ":optional", true, true},
		{[]string{`input type="hidden" required`}, ":optional", true, true},
		{[]string{"input readonly"}, ":read-only", true, true},
		{[]string{"input readonly"}, ":read-write", false, false},
		{[]string{"input"}, ":read-write", true, true},
		{[]string{`input type="checkbox"`}, ":read-only", true, true},
		{[]string{"div contenteditable", "p"}, "p:read-write", true, true},
		{[]string{`div contenteditable="false"`, "p"}, ":read-only", true, true},
		{[]string{"div"}, ":read-only", true, true},
		{[]string{`input placeholder="Search"`}, ":placeholder-shown", true, true},
		{[]string{`input placeholder="Search" value="go"`}, ":placeholder-shown", false, false},
		{[]string{`textarea placeholder="Say"`}, ":placeholder-shown", false, true},
		{[]string{"input"}, ":placeholder-shown", false, false},
		{[]string{"br"}, ":empty", true, true},
		{[]string{"p"}, ":empty", false, true},
		// The reader draws its own toggle for <details>, so a hiding rule never
		// decides on the open state (TestWebCSSDetailsBodyKeptForTheToggle).
		{[]string{"details open"}, ":open", false, true},
		{[]string{"details"}, ":open", false, false},
		{[]string{"dialog open"}, ":open", true, true},
		{[]string{"details open"}, "[open]", false, true},
		{[]string{"dialog"}, "dialog:not([open])", true, true},
		{[]string{`html lang="fr-CA"`, "p"}, "p:lang(fr)", true, true},
		{[]string{`html lang="fr-CA"`, "p"}, `p:lang("fr-CA")`, true, true},
		{[]string{`html lang="fr-CA"`, "p"}, "p:lang(en)", false, false},
		{[]string{"p"}, ":lang(en)", false, true},
		{[]string{`div dir="rtl"`, "p"}, "p:dir(rtl)", true, true},
		{[]string{`div dir="rtl"`, "p"}, "p:dir(ltr)", false, false},
		{[]string{"p"}, ":dir(ltr)", true, true},
		{[]string{"html"}, ":root", true, true},
		{[]string{"html", "body"}, "body:root", false, false},
		{[]string{"p"}, ":root", false, true}, // a page that left out <html>
		{[]string{`a href="/x"`}, ":link", true, true},
		{[]string{`a href="/x"`}, ":any-link", true, true},
		{[]string{"a"}, ":link", false, false},
		{[]string{`area href="/x"`}, ":link", true, true},
		{[]string{"div"}, ":defined", true, true},
		{[]string{"my-card"}, ":defined", false, true},
		{[]string{"my-card"}, ":not(:defined)", false, true},
		{[]string{"p"}, ":first-child", false, true},
		{[]string{"p"}, ":nth-child(2n+1)", false, true},
		{[]string{"p"}, ":has(img)", false, true},
		{[]string{`p class="a"`}, ":is(.a, .b)", true, true},
		{[]string{`p class="a"`}, ":matches(.b, .a)", true, true},
		{[]string{`p class="a"`}, ":where(.b)", false, false},
		{[]string{`p class="a"`}, ":not(.a)", false, false},
		{[]string{`p class="a"`}, ":not(.b)", true, true},
		{[]string{`p class="a"`}, ":is(div .a)", false, true},
		{[]string{`p class="a"`}, ":not(div .a)", false, true},
		{[]string{"p"}, ":-webkit-any(p, div)", true, true},
	}
	for _, tc := range cases {
		chain := cssmChain(tc.chain...)
		if got := cssmMatch(tc.selector, chain, true); got != tc.exact {
			t.Errorf("%q on %q for a hiding rule = %t, want %t", tc.selector, tc.chain, got, tc.exact)
		}
		if got := cssmMatch(tc.selector, chain, false); got != tc.lenient {
			t.Errorf("%q on %q for another rule = %t, want %t", tc.selector, tc.chain, got, tc.lenient)
		}
	}
}

// :root is the <html> element only — its custom properties still reach the
// whole page — and :link is an anchor with an href.
func TestWebCSSRootAndLink(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
:root { --accent: #00ff00; }
p { color: var(--accent); }
a:link { color: #0000ff; }
a:visited { display: none; }
:root > p { display: none; }
</style></head><body><p>Accent text <a href="/next">Next page</a> <a>No href</a></p></body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Accent text", "Next page", "No href"}, nil)
	if run := cssmRun(t, doc, "Accent text"); !run.Style.HasColor || run.Style.Color != cssmGreen {
		t.Errorf("a :root custom property did not reach the paragraph: colour %+v", run.Style.Color)
	}
	if run := cssmRun(t, doc, "Next page"); !run.Style.HasColor || run.Style.Color != cssmBlue {
		t.Errorf("a:link did not colour the link: colour %+v", run.Style.Color)
	}
	if run := cssmRun(t, doc, "No href"); run.Style.Color != cssmGreen {
		t.Errorf("a:link coloured an anchor without href: colour %+v", run.Style.Color)
	}
}

// Escaped class names are decoded: Tailwind's `.md\:hidden` is the class
// "md:hidden", not the class "md".
func TestWebCSSEscapedClassNames(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
.md\:hidden { display: none; }
.w-1\/2 { font-weight: bold; }
</style></head><body>
<p class="md:hidden">Mobile only</p>
<p class="md">Plain md</p>
<p class="w-1/2">Half width</p>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Plain md", "Half width"}, []string{"Mobile only"})
	if run := cssmRun(t, doc, "Half width"); !run.Style.Bold {
		t.Error(`.w-1\/2 did not match class "w-1/2"`)
	}
}

// A selector list is split on its top-level commas only, so :is() and :not()
// keep their argument lists; the rule records whether it would hide.
func TestWebCSSSelectorListsAndIs(t *testing.T) {
	rules := parseWebCSS(`:is(.a, .b), [title="x,y"] { color: red }
[hidden] { display: none !important }
.y { visibility: var(--v) }
.z { display: block }`)
	var selectors []string
	hides := map[string]bool{}
	for _, rule := range rules {
		selectors = append(selectors, rule.selector)
		hides[rule.selector] = rule.hides
	}
	if got := strings.Join(selectors, " | "); got != `:is(.a, .b) | [title="x,y"] | [hidden] | .y | .z` {
		t.Errorf("selectors = %s", got)
	}
	for selector, want := range map[string]bool{`:is(.a, .b)`: false, "[hidden]": true, ".y": true, ".z": false} {
		if hides[selector] != want {
			t.Errorf("%s hides = %t, want %t", selector, hides[selector], want)
		}
	}

	doc := parseWebPage(`<html><head><style>
:is(.ad, .promo) { display: none; }
p:not(.keep, .also-keep) { font-style: italic; }
</style></head><body>
<p class="ad">Advert</p><p class="promo">Promo</p><p class="keep">Kept text</p><p class="other">Other text</p>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Kept text", "Other text"}, []string{"Advert", "Promo"})
	if run := cssmRun(t, doc, "Other text"); !run.Style.Italic {
		t.Error("p:not(.keep, .also-keep) skipped an unrelated paragraph")
	}
	if run := cssmRun(t, doc, "Kept text"); run.Style.Italic {
		t.Error("p:not(.keep, .also-keep) matched .keep")
	}
}

// An element's attributes are parsed at most once, only when a selector asks
// for one, and every copy of the element shares the result.
func TestWebCSSAttributesParsedLazilyOnce(t *testing.T) {
	el := webNewElement("div", ` class="card" data-state="open"`)
	if el.attrs == nil || el.attrs.parsed {
		t.Fatal("a fresh element must carry its raw attribute text, unparsed")
	}
	chain := []webElement{el}
	if cssmMatch(".other[data-state]", chain, false) {
		t.Fatal(".other[data-state] matched a div without that class")
	}
	if el.attrs.parsed {
		t.Error("a compound whose class already failed parsed the attributes anyway")
	}
	if !cssmMatch(`[data-state="open"]`, chain, true) {
		t.Fatal(`[data-state="open"] did not match`)
	}
	if !el.attrs.parsed || len(el.attrs.list) != 2 {
		t.Errorf("attributes after the first attribute test: %+v", *el.attrs)
	}
	if copied := el; copied.attrs != el.attrs {
		t.Error("a copy of the element does not share its attribute set")
	}
	if bare := webNewElement("p", "  "); bare.attrs != nil {
		t.Error("an element without attributes allocated an attribute set")
	}
}

// Every sheet is in force for the whole page: a <style> after the content still
// styles it, an unclosed <head> no longer stands between <html> and the body
// (whose :root custom property then reaches the paragraphs), and a <style>
// inside <noscript> or <template> stays out, as before.
func TestWebCSSInlineSheetsApplyToTheWholePage(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
:root { --accent: #00ff00; }
p { color: var(--accent); }
</style><body>
<p>Unclosed head</p>
<p class="late">Late styled</p>
<noscript><style>p { display: none; }</style></noscript>
<template><style>p { display: none; }</style></template>
<style>.late { color: #0000ff; }</style>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Unclosed head", "Late styled"}, nil)
	if run := cssmRun(t, doc, "Unclosed head"); !run.Style.HasColor || run.Style.Color != cssmGreen {
		t.Errorf("a :root custom property did not get past an unclosed <head>: colour %+v", run.Style.Color)
	}
	if run := cssmRun(t, doc, "Late styled"); !run.Style.HasColor || run.Style.Color != cssmBlue {
		t.Errorf("a <style> after the paragraph did not style it: colour %+v", run.Style.Color)
	}

	var b strings.Builder
	webCollectInlineCSS(`<style>a{}</style><!-- <style>b{}</style> --><script>"<style>c{}</style>"</script><svg><style>d{}</style></svg><STYLE media="screen">e{}</STYLE>`, &b)
	if got := b.String(); got != "\na{}\ne{}" {
		t.Errorf("collected sheets = %q, want only a{} and e{}", got)
	}
}

// :target is the element the address's fragment names. A rule that reveals
// (display other than none, visibility: visible) still applies through :hover
// or :focus-within, which never hold on the reader, so a sub-menu or tip a page
// opens that way stays reachable; any other :hover rule still never applies.
func TestWebCSSTargetAndRevealStates(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
.panel { display: none; }
.panel:target { display: block; }
.menu ul { display: none; }
.menu li:hover > ul { display: block; }
.tip { visibility: hidden; }
.field:focus-within .tip { visibility: visible; }
a:hover { color: #ff0000; }
</style></head><body>
<div class="panel" id="faq1">First answer</div>
<div class="panel" id="faq2">Second answer</div>
<ul class="menu"><li>Products<ul><li>Sub item</li></ul></li></ul>
<div class="field"><span class="tip">Tip text</span></div>
<p><a href="/x">Plain link</a></p>
</body></html>`, "", cssmURL+"#faq2")
	cssmExpect(t, doc,
		[]string{"Second answer", "Products", "Sub item", "Tip text", "Plain link"},
		[]string{"First answer"})
	if run := cssmRun(t, doc, "Plain link"); run.Style.HasColor && run.Style.Color == cssmRed {
		t.Error("a:hover coloured a link nobody is pointing at")
	}

	rules := parseWebCSS(`.a:hover { display: block } .b:hover { color: red } .c { visibility: visible } .d { opacity: 1 } .e { opacity: .8 } .f { display: none }`)
	reveals := map[string]bool{}
	for _, rule := range rules {
		reveals[rule.selector] = rule.reveals
	}
	for selector, want := range map[string]bool{".a:hover": true, ".b:hover": false, ".c": true, ".d": true, ".e": false, ".f": false} {
		if reveals[selector] != want {
			t.Errorf("%s reveals = %t, want %t", selector, reveals[selector], want)
		}
	}
	chain := cssmChain("html", "body", `li class="x"`)
	sel, _ := webCompileSelector("li:hover")
	if sel.matchesIn(chain, webSelLenient) || sel.matchesIn(chain, webSelStrict) || !sel.matchesIn(chain, webSelReveal) {
		t.Error("li:hover must hold only for a revealing rule")
	}
}

// Media queries are answered for one screen — the one page scripts see —
// instead of every width query applying at once.
func TestWebCSSMediaQueries(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"", true},
		{"@media screen", true},
		{"all", true},
		{"print", false},
		{"speech", false},
		{"not print", true},
		{"screen and (max-width: 600px)", false},
		{"screen and (min-width: 1024px)", true},
		{"only screen and (min-width: 1024px)", true},
		{"(min-width: 48em)", true},
		{"(max-width: 767.98px)", false},
		{"(min-width: 768px) and (max-width: 1024px)", false},
		{"(max-width: 600px), (min-width: 1200px)", true},
		{"(max-width: 600px), print", false},
		{"not all and (min-width: 768px)", false},
		{"not all and (min-width: 1600px)", true},
		{"(width < 48rem)", false},
		{"(width >= 48rem)", true},
		{"(400px <= width <= 700px)", false},
		{"(1000px <= width <= 1300px)", true},
		{"(48rem > width)", false},
		{"(orientation: landscape)", true},
		{"(orientation: portrait)", false},
		{"(min-aspect-ratio: 16/9)", true},
		{"(max-aspect-ratio: 4/3)", false},
		{"(min-height: 800px)", false},
		{"(height <= 720px)", true},
		{"((min-width: 500px) and (max-width: 1300px))", true},
		{"(min-width: 2000px) or (orientation: landscape)", true},
		{"not (min-width: 2000px)", true},
		{"(prefers-color-scheme: dark)", true}, // a feature the reader does not model applies
		{"(hover: hover) and (pointer: fine)", true},
		{"(-webkit-min-device-pixel-ratio: 2)", true},
		{"(min-width: calc(100px + 1em))", true}, // and so does a value it cannot read
	}
	for _, tc := range cases {
		if got := webMediaQueryApplies(tc.query, 1280, 720); got != tc.want {
			t.Errorf("%q at 1280×720 = %t, want %t", tc.query, got, tc.want)
		}
	}
	if !webMediaQueryApplies("(max-width: 600px)", 480, 800) || !webMediaQueryApplies("(orientation: portrait)", 480, 800) {
		t.Error("a narrow portrait screen does not match its own queries")
	}

	// Tailwind's paired variants: exactly one copy of the content stays.
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1280, 720
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	doc := parseWebPage(`<html><head><style>
@media (min-width: 768px) { .md\:hidden { display: none; } }
@media not all and (min-width: 768px) { .max-md\:hidden { display: none; } }
@media print { .screen-only { display: none; } }
</style></head><body>
<p class="max-md:hidden">Desktop copy</p>
<p class="md:hidden">Mobile copy</p>
<p class="screen-only">Screen text</p>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Desktop copy", "Screen text"}, []string{"Mobile copy"})
}

// opacity: 0 hides as surely as display: none (the reader paints the fade), so
// it is matched strictly too: an undecidable `:not(:defined)` no longer fades
// every custom element out, while a class that names its element still does.
func TestWebCSSOpacityZeroIsMatchedStrictly(t *testing.T) {
	rules := parseWebCSS(`.a { opacity: 0 } .b { opacity: .5 } .c { opacity: var(--o) } .d { opacity: 0% } .e { opacity: 1 }`)
	hides := map[string]bool{}
	for _, rule := range rules {
		hides[rule.selector] = rule.hides
	}
	for selector, want := range map[string]bool{".a": true, ".b": false, ".c": true, ".d": true, ".e": false} {
		if hides[selector] != want {
			t.Errorf("%s hides = %t, want %t", selector, hides[selector], want)
		}
	}

	doc := parseWebPage(`<html><head><style>
:not(:defined) { opacity: 0; }
.ghost { opacity: 0; }
</style></head><body>
<my-widget>Widget text</my-widget>
<p class="ghost">Ghost text</p>
</body></html>`, "", cssmURL)
	if run := cssmRun(t, doc, "Widget text"); run.Style.HasOpacity && run.Style.Opacity == 0 {
		t.Error(":not(:defined) faded out a custom element it cannot decide")
	}
	if run := cssmRun(t, doc, "Ghost text"); !run.Style.HasOpacity || run.Style.Opacity != 0 {
		t.Errorf(".ghost no longer faded its element: %+v", run.Style)
	}
}

// :not() takes neither a pseudo-element nor an empty list: a browser drops such
// a rule whole, where the matcher used to read it as "not this" and match every
// element. :is() forgives one; an argument that cannot match adds no
// specificity either way.
func TestWebCSSNotValidity(t *testing.T) {
	for _, selector := range []string{":not(::before)", ":not()", "p:not(.a, ::after)", "p:not(::marker) span"} {
		if sel, _ := webCompileSelector(selector); !sel.never {
			t.Errorf("%q compiled to a selector that can match", selector)
		}
	}
	chain := cssmChain("html", "body", `p class="a"`)
	if !cssmMatch(":is(::before, .a)", chain, true) || cssmMatch(":is(::before)", chain, false) {
		t.Error(":is() did not forgive a pseudo-element argument")
	}
	spec := func(selector string) int {
		_, n := webCompileSelector(selector)
		return n
	}
	if spec(":is(::before, .a)") != spec(".a") || spec(":is(::before)") != spec("*") {
		t.Error("a pseudo-element argument added specificity")
	}
	// An argument with a combinator is decided "no" when its subject fails.
	if cssmMatch(":is(div .b)", chain, false) || !cssmMatch(":not(div .b)", chain, true) {
		t.Error(":is(div .b) was not ruled out by its subject")
	}
}

// The reader draws its own toggle for <details> and keeps the section's body
// either way; a sheet's `details:not([open]) > :not(summary)` (Primer,
// normalize) must not drop that body's text, or opening the section shows
// nothing. A closed <dialog> stays hidden.
func TestWebCSSDetailsBodyKeptForTheToggle(t *testing.T) {
	doc := parseWebPage(`<html><head><style>
details:not([open]) > :not(summary) { display: none !important; }
dialog:not([open]) { display: none; }
</style></head><body>
<details><summary>Show more</summary><p>Details body</p></details>
<dialog><p>Closed dialog</p></dialog>
</body></html>`, "", cssmURL)
	cssmExpect(t, doc, []string{"Show more", "Details body"}, []string{"Closed dialog"})
}
