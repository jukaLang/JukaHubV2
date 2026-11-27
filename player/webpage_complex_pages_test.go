package main

// webpage_complex_pages_test.go keeps two failures the real CNN and Yahoo
// pages showed out of the reader, both as page-path fixtures (parse -> image
// pass -> browserPageLayout -> drawWebDocument):
//
//   - a card list whose <ul> is itself a grid item (`grid-area: 1/1/auto/-1`,
//     as CNN's standard card list is) used to hand that cell down to every
//     descendant, so each card's link, headline and summary block asked for
//     the same grid cell and their words drew on top of each other;
//   - an empty <button> (a search bar's magnifier drawn by a background image,
//     or Yahoo's icon-only nav buttons) used to be labelled "Submit", which
//     put a word in the flow the page never wrote.

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// cnnCardListHTML is the shape of CNN's standard card list: a grid <ul> that
// is itself placed in its wrapper's grid, cards that separate with a top rule
// and one column, and a first card whose <li> is display: contents so its two
// links are the grid's own items.
const cnnCardListHTML = `<style>
body{margin:0;font-family:sans-serif}
.field-wrapper{display:grid;grid-template-columns:1fr;align-items:start}
.container_standard__field-links{display:grid;grid-area:1/1/auto/-1;grid-template-columns:1fr;row-gap:16px;margin:0;padding:0;list-style-type:none}
.container__item{height:100%;list-style:none}
.container_standard__item:first-of-type{display:contents}
.container_standard__item:not(:first-of-type){padding-top:16px;border-top:1px solid #e6e6e6;grid-column:1}
.container__link{display:inline-block;width:100%;color:inherit;text-decoration:none}
.container_standard__headline{display:flex;flex-direction:column;margin:0;font-size:22px;font-weight:700}
.container_standard__description{font-size:14px;margin:6px 0 16px}
</style>
<div class="field-wrapper">
<ul class="container__field-links container_standard__field-links">
<li class="card container__item container_standard__item">
<a class="container__link" href="#one"><div class="container__text"><div class="container_standard__headline"><span>Oil prices are again rising</span></div><div class="container_standard__description">Brent crude futures climbed this week</div></div></a>
</li>
<li class="card container__item container_standard__item">
<a class="container__link" href="#two"><div class="container__text"><div class="container_standard__headline"><span>Houthis strike Saudi oil infrastructure</span></div><div class="container_standard__description">Riyadh intensifies attacks on Yemen</div></div></a>
</li>
<li class="card container__item container_standard__item">
<a class="container__link" href="#three"><div class="container__text"><div class="container_standard__headline"><span>Trump backs Iraq's oil resources</span></div><div class="container_standard__description">Washington aligns with Baghdad</div></div></a>
</li>
</ul>
</div>`

// TestBrowserHeadlessCardHeadlinesTakeTheirOwnLines is the CNN card list: each
// headline is a block in its own card, so no two of them may share a line or a
// rectangle. Before grid placement stopped being inherited, all three cards
// claimed cell 1/1 and the second and third drew over each other.
func TestBrowserHeadlessCardHeadlinesTakeTheirOwnLines(t *testing.T) {
	h := newHeadlessBrowser(t)
	doc, layout := h.load("complex-card-headlines", cnnCardListHTML)

	// Layout runs are split per word, so each headline is found by the first
	// word of its own line (the three headlines share no first word).
	headlines := []string{"Oil", "Houthis", "Trump"}
	rects := make([]sdl.Rect, len(headlines))
	for i, text := range headlines {
		rects[i] = h.textRectPrefix(layout, text)
		if rects[i].W <= 0 || rects[i].H <= 0 {
			t.Fatalf("headline %q measured no box: %+v", text, rects[i])
		}
	}
	for i := 0; i < len(rects); i++ {
		for j := i + 1; j < len(rects); j++ {
			if headlessOverlap(rects[i], rects[j], 0) {
				t.Fatalf("headlines %q and %q overlap: %+v %+v", headlines[i], headlines[j], rects[i], rects[j])
			}
			if rects[j].Y <= rects[i].Y {
				t.Fatalf("headline %q (y=%d) is not below %q (y=%d)", headlines[j], rects[j].Y, headlines[i], rects[i].Y)
			}
		}
	}
	// The cards also keep their own descriptions under their own headlines.
	for i, text := range []string{"Brent", "Riyadh", "Washington"} {
		rect := h.textRectPrefix(layout, text)
		if rect.W <= 0 {
			t.Fatalf("description %q vanished", text)
		}
		if rect.Y <= rects[i].Y {
			t.Fatalf("description %q (y=%d) is not below its headline %q (y=%d)", text, rect.Y, headlines[i], rects[i].Y)
		}
	}
	// The rule itself: a container's placement is not a property of the boxes
	// inside it, so no headline may carry a cell of its own.
	for _, text := range []string{"Oil prices are again rising", "Houthis strike Saudi oil infrastructure", "Trump backs Iraq's oil resources"} {
		st, ok := webTestStyleForText(doc, text)
		if !ok {
			t.Fatalf("headline %q missing from the parsed document", text)
		}
		if st.GridRowStart != 0 || st.GridColumnStart != 0 || st.GridArea != "" {
			t.Fatalf("headline %q inherited its container's grid placement: row=%d col=%d area=%q",
				text, st.GridRowStart, st.GridColumnStart, st.GridArea)
		}
	}
}

// cnnHeaderHTML is CNN's header search form: the submit button is an empty
// <button> with a title and a background magnifier, so it has no text of its
// own and the reader must not say "Submit" in its place.
const cnnHeaderHTML = `<style>
body{margin:0;font-family:sans-serif}
.header__container{display:flex;align-items:center;padding:12px 16px;border-bottom:1px solid #ddd}
.search-bar__form{display:flex;align-items:center;gap:8px}
.search-bar__input{width:240px;height:32px}
.search-bar__submit{width:42px;height:32px;border:none}
</style>
<header><div class="header__container">
<button class="header__menu-icon" aria-label="Navigation menu">Menu</button>
<form action="/search" class="search-bar__form" role="search">
<input placeholder="Search CNN..." aria-label="Search" class="search-bar__input" type="text" name="q">
<button type="submit" class="search-bar__submit" title="Submit" aria-label="Submit"></button>
</form>
</div></header>`

// yahooSearchHTML is a Yahoo static region with the same pattern: the search
// box and the "Feed view" toggle are icon-only buttons, and the words in the
// region are only the ones the page wrote.
const yahooSearchHTML = `<style>
body{margin:0;font-family:sans-serif}
.ybar{display:flex;align-items:center;gap:12px;padding:14px 16px}
.ybar__form{display:flex;align-items:center;flex:1;border-radius:999px;border:1px solid #d8d8e0;padding:4px 8px}
.ybar__input{flex:1;border:none;height:30px}
.ybar__submit,.ybar__feed{width:36px;height:36px;border:none;border-radius:999px}
</style>
<div class="ybar">
<form class="ybar__form" role="search" action="/search">
<input class="ybar__input" type="search" placeholder="Search the web" aria-label="Search">
<button class="ybar__submit" type="submit" aria-label="Search"></button>
</form>
<button class="ybar__feed" type="button" aria-label="Feed view"></button>
</div>`

func headlessHasText(layout *webLayout, want string) bool {
	for _, text := range headlessRunTexts(layout) {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

// TestBrowserHeadlessIconOnlyButtonsAddNoWords is both pages' icon buttons: an
// empty <button> draws nothing of the page's text, while the words the page
// did write (a field's placeholder, a real label) still show.
func TestBrowserHeadlessIconOnlyButtonsAddNoWords(t *testing.T) {
	cases := []struct {
		name, html, placeholder, label string
	}{
		{"complex-cnn-header", cnnHeaderHTML, "Search CNN...", "Menu"},
		{"complex-yahoo-search", yahooSearchHTML, "Search the web", ""},
	}
	for _, c := range cases {
		h := newHeadlessBrowser(t)
		_, layout := h.load(c.name, c.html)
		if headlessHasText(layout, "Submit") {
			t.Fatalf("%s: an empty icon button drew the word Submit: %v", c.name, headlessRunTexts(layout))
		}
		// The placeholder is the field's prompt: proof the form itself still
		// rendered around the button.
		if rect, ok := headlessRunPrefixRect(layout, c.placeholder); !ok || rect.W <= 0 {
			t.Fatalf("%s: the field's placeholder %q vanished: %v", c.name, c.placeholder, headlessRunTexts(layout))
		}
		if c.label != "" && !headlessHasText(layout, c.label) {
			t.Fatalf("%s: the button label %q vanished with the invented word: %v", c.name, c.label, headlessRunTexts(layout))
		}
	}
}

// TestBrowserHeadlessInputSubmitKeepsItsDefaultLabel guards the other side of
// the rule: <input type="submit"> with no value does have a default label, and
// HTML calls it "Submit" — only an empty <button> has none.
func TestBrowserHeadlessInputSubmitKeepsItsDefaultLabel(t *testing.T) {
	h := newHeadlessBrowser(t)
	_, layout := h.load("complex-input-submit", `<style>body{margin:0;font-family:sans-serif}</style>
	<form action="/save"><input type="text" placeholder="Name"><input type="submit"></form>`)
	if !headlessHasText(layout, "Submit") {
		t.Fatalf("<input type=submit> lost its default label: %v", headlessRunTexts(layout))
	}
}
