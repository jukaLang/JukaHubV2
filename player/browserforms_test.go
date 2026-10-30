package main

import (
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

// The reader used to drop every <input> as a void tag, so a search page came
// out with no search box at all: there was nothing on screen to type into. These
// tests hold the whole input path together — a field is drawn, it takes a value,
// and the value reaches the page the form named.
const browserFormHTML = `<html><body>
<form action="/lite/" method="post">
<input type="text" name="q" placeholder="Search the web">
<select name="kl"><option value="us">United States</option><option value="de" selected>Germany</option></select>
<input type="checkbox" name="safe" value="1" checked>Safe search
<input type="submit" value="Search">
</form>
<form action="/notes">
<textarea name="note" placeholder="Write a note"></textarea>
<input type="submit" value="Send">
</form>
</body></html>`

// browserFormDoc parses the fixture at the address a form submit would resolve
// its action against.
func browserFormDoc() *webDocument {
	return parseWebPage(browserFormHTML, "", "https://lite.duckduckgo.com/lite/")
}

// browserFormClearEvents drops input queued by an earlier test. The keyboard
// modal drains whatever is pending, so a stray Return or Escape left behind by
// another test would confirm or cancel this one's field instead of the events it
// queued itself.
func browserFormClearEvents() {
	for sdl.PollEvent() != nil {
	}
}

// browserFormControlsOf returns the fixture's controls by kind, in page order.
func browserFormControlsOf(t *testing.T, elem Element) []*webControl {
	t.Helper()
	browserFormClearEvents()
	// A load started by an earlier test publishes its page from its own
	// goroutine, so its document could land on top of this fixture's. Land
	// them first, then install the page under test.
	waitForBrowserFetches()
	doc := browserFormDoc()
	setBrowserDocument(elem.Variable, doc)
	controls := browserFormControls(elem)
	if len(controls) == 0 {
		t.Fatal("the page drew no form controls")
	}
	return controls
}

func browserFormControl(t *testing.T, elem Element, kind, name string) *webControl {
	t.Helper()
	for _, ctrl := range browserFormControlsOf(t, elem) {
		if ctrl.Kind == kind && ctrl.Name == name {
			return ctrl
		}
	}
	t.Fatalf("no %s control named %q", kind, name)
	return nil
}

// browserFormFetches records every URL the page load seam was asked for. The
// page cache starts empty: a form submitted twice in one suite produces the
// same address, and a cached hit never reaches the seam, which would read as
// "nothing was submitted".
func browserFormFetches(t *testing.T) func() []string {
	t.Helper()
	browserClearPageCache()
	t.Cleanup(browserClearPageCache)
	var mu sync.Mutex
	var seen []string
	restore := setBrowserPageFetcherForTest(func(rawURL string) browserPage {
		mu.Lock()
		seen = append(seen, rawURL)
		mu.Unlock()
		return browserPage{Text: "Title: results\n\nresults for " + rawURL, FinalURL: rawURL}
	})
	t.Cleanup(func() {
		waitForBrowserFetches()
		restore()
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// browserFormQueryOf is the last fetch's query, parsed, so a test can assert on
// fields without depending on the order url.Values encodes them in.
func browserFormQueryOf(t *testing.T, fetches func() []string) (string, url.Values) {
	t.Helper()
	waitForBrowserFetches()
	got := fetches()
	if len(got) == 0 {
		t.Fatal("the form submitted nothing")
	}
	last := got[len(got)-1]
	parsed, err := url.Parse(last)
	if err != nil {
		t.Fatalf("submitted %q: %v", last, err)
	}
	return parsed.Path, parsed.Query()
}

// A form is content: the field, the list, the box and the button all have to be
// on the page, reachable with the D-pad, and shown as what they are.
func TestBrowserFormControlsAreDrawn(t *testing.T) {
	browserEditURLStubFetcher(t)
	config, _, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	doc := browserFormDoc()
	setBrowserDocument(elem.Variable, doc)

	layout := browserPageLayout(config, elem)
	if layout == nil {
		t.Fatal("the page did not lay out")
	}
	kinds := map[string]int{}
	for _, hit := range layout.Links {
		if hit.Control != nil {
			kinds[hit.Control.Kind]++
		}
	}
	for _, want := range []string{"text", "select", "check", "submit", "textarea"} {
		if kinds[want] == 0 {
			t.Errorf("no %s control among the page's cursor stops: %v", want, kinds)
		}
	}

	// The placeholder stands in for an empty field, and the select shows the
	// option the markup preselected rather than the first one.
	if got := browserFormControl(t, elem, "text", "q").Display(); !strings.Contains(got, "Search the web") {
		t.Errorf("empty field shows %q, want its placeholder", got)
	}
	sel := browserFormControl(t, elem, "select", "kl")
	if got := sel.Display(); !strings.Contains(got, "Germany") {
		t.Errorf("select shows %q, want the selected option", got)
	}
	if got := sel.submitValue(); got != "de" {
		t.Errorf("select submits %q, want the option's value", got)
	}
	if !browserFormControl(t, elem, "check", "safe").Checked {
		t.Error("a checked box read back as unticked")
	}
	area := browserFormControl(t, elem, "textarea", "note")
	if !strings.Contains(area.Display(), "Write a note") {
		t.Errorf("textarea shows %q, want its placeholder", area.Display())
	}
}

// Typing in a field and confirming sends the form: the action, then the field's
// own name. That is the press a search box exists for.
func TestBrowserFormFieldSubmitSendsTypedValue(t *testing.T) {
	fetches := browserFormFetches(t)
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	ctrl := browserFormControl(t, elem, "text", "q")

	sdl.PushEvent(&queuedTextInput{TextInputEvent: sdl.TextInputEvent{Type: sdl.TEXTINPUT, Text: [32]byte{'c', 'a', 't', 's'}}})
	sdl.PushEvent(&queuedKeyboard{KeyboardEvent: sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RETURN}}})
	browserFillPageControl(renderer, config, elem, ctrl)

	path, query := browserFormQueryOf(t, fetches)
	if path != "/lite/" {
		t.Errorf("submitted to %q, want the form's action", path)
	}
	if got := query.Get("q"); got != "cats" {
		t.Errorf("q = %q, want the typed value", got)
	}
	// The rest of the form travels with it: the page's filter and its option.
	if got := query.Get("kl"); got != "de" {
		t.Errorf("kl = %q, want the selected option's value", got)
	}
	if got := query.Get("safe"); got != "1" {
		t.Errorf("safe = %q, want the ticked box's value", got)
	}
}

// Pressing the Submit button sends what the fields hold. It used to open the
// keyboard on the button itself, so confirming submitted the button's own label
// ("q=Search") and the page searched for the word Search.
func TestBrowserSubmitButtonSendsTheFilledField(t *testing.T) {
	fetches := browserFormFetches(t)
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	controls := browserFormControlsOf(t, elem)
	var submit *webControl
	for _, ctrl := range controls {
		if ctrl.Kind == "submit" && ctrl.Name == "" && ctrl.Value == "Search" {
			submit = ctrl
		}
		if ctrl.Kind == "text" {
			ctrl.Value = "cats"
		}
	}
	if submit == nil {
		t.Fatal("no Search button on the page")
	}

	browserActivatePageControl(renderer, config, elem, submit)

	if virtualKeyboardActive {
		t.Error("the Submit button opened a keyboard instead of sending the form")
	}
	_, query := browserFormQueryOf(t, fetches)
	if got := query.Get("q"); got != "cats" {
		t.Errorf("q = %q, want the value already in the field", got)
	}
	if got := query.Get("Search"); got != "" {
		t.Errorf("the button's label was sent as a field: %q", got)
	}
}

// A submit with nothing filled in opens the form's own field: sending an empty
// search is a wasted page load, and the press the user meant is on the box.
func TestBrowserSubmitOnEmptyFormOpensTheField(t *testing.T) {
	fetches := browserFormFetches(t)
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	var submit *webControl
	for _, ctrl := range browserFormControlsOf(t, elem) {
		if ctrl.Kind == "submit" && ctrl.Value == "Search" {
			submit = ctrl
		}
	}
	if submit == nil {
		t.Fatal("no Search button on the page")
	}

	// Escape leaves the keyboard the submit is expected to open; the assertion
	// is about which control it opened on.
	sdl.PushEvent(&queuedKeyboard{KeyboardEvent: sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_ESCAPE}}})
	browserActivatePageControl(renderer, config, elem, submit)

	if seen := fetches(); len(seen) != 0 {
		t.Fatalf("an empty form was submitted: %v", seen)
	}
	value, _ := config.Variables.Custom[browserFormVariable].(string)
	if value != "" {
		t.Errorf("the field opened holding %q, want the empty value the page shipped", value)
	}
}

// A <select> has no keyboard on a handheld, so it steps in place. The press
// that sends it must carry the option's value, not its label.
func TestBrowserSelectStepsAndSendsItsValue(t *testing.T) {
	fetches := browserFormFetches(t)
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	sel := browserFormControl(t, elem, "select", "kl")
	// The markup preselects Germany; one step wraps to the United States.
	browserActivatePageControl(renderer, config, elem, sel)
	if got := sel.submitValue(); got != "us" {
		t.Fatalf("after one step the select submits %q, want the next option", got)
	}
	if got := sel.Display(); !strings.Contains(got, "United States") {
		t.Errorf("the control shows %q, want the stepped-to option", got)
	}
	// The layout must have been re-measured, or the screen still draws Germany.
	if got := strings.Join(browserLayoutControlTexts(config, elem), "|"); !strings.Contains(got, "United States") {
		t.Errorf("the drawn page still shows the old option: %q", got)
	}

	for _, ctrl := range browserFormControls(elem) {
		if ctrl.Kind == "text" {
			ctrl.Value = "cats"
		}
	}
	var submit *webControl
	for _, ctrl := range browserFormControls(elem) {
		if ctrl.Kind == "submit" && ctrl.Value == "Search" {
			submit = ctrl
		}
	}
	browserActivatePageControl(renderer, config, elem, submit)

	_, query := browserFormQueryOf(t, fetches)
	if got := query.Get("kl"); got != "us" {
		t.Errorf("kl = %q, want the stepped-to option's value", got)
	}
}

// A tick box toggles where it is, and only a ticked one is sent.
func TestBrowserCheckboxTogglesAndOnlyTickedIsSent(t *testing.T) {
	fetches := browserFormFetches(t)
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	box := browserFormControl(t, elem, "check", "safe")
	if !box.Checked {
		t.Fatal("the fixture's checked box read back as unticked")
	}
	browserActivatePageControl(renderer, config, elem, box)
	if box.Checked {
		t.Fatal("pressing the box did not untick it")
	}
	if got := box.Display(); !strings.Contains(got, "□") {
		t.Errorf("an unticked box draws %q", got)
	}

	for _, ctrl := range browserFormControls(elem) {
		if ctrl.Kind == "text" {
			ctrl.Value = "cats"
		}
	}
	var submit *webControl
	for _, ctrl := range browserFormControls(elem) {
		if ctrl.Kind == "submit" && ctrl.Value == "Search" {
			submit = ctrl
		}
	}
	browserActivatePageControl(renderer, config, elem, submit)

	_, query := browserFormQueryOf(t, fetches)
	if got, ok := query["safe"]; ok {
		t.Errorf("an unticked box was sent: %v", got)
	}
}

// A textarea is a field like any other: it shows the text the page shipped, and
// what is typed into it is sent under its own name.
func TestBrowserTextareaTakesAValue(t *testing.T) {
	fetches := browserFormFetches(t)
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	area := browserFormControl(t, elem, "textarea", "note")
	if area.Kind != "textarea" || area.Name != "note" {
		t.Fatalf("textarea came out as %+v", area)
	}
	// A textarea's value is the text between its tags, so the fixture's empty
	// one has nothing but its placeholder until it is typed into.
	if got := area.Display(); !strings.Contains(got, "Write a note") {
		t.Errorf("an empty textarea shows %q, want its placeholder", got)
	}
	area.Value = "buy milk"
	if got := area.Display(); !strings.Contains(got, "buy milk") {
		t.Errorf("a filled textarea shows %q", got)
	}
	// It keeps that value as it opens, so what is typed has to replace it: a
	// field that opened holding the page's text starts selected, the way the
	// address bar does.

	sdl.PushEvent(&queuedTextInput{TextInputEvent: sdl.TextInputEvent{Type: sdl.TEXTINPUT, Text: [32]byte{'h', 'i'}}})
	sdl.PushEvent(&queuedKeyboard{KeyboardEvent: sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_RETURN}}})
	browserFillPageControl(renderer, config, elem, area)

	path, query := browserFormQueryOf(t, fetches)
	if path != "/notes" {
		t.Errorf("submitted to %q, want the textarea's own form", path)
	}
	if got := query.Get("note"); got != "hi" {
		t.Errorf("note = %q, want the typed value", got)
	}
	if got := query.Get("q"); got != "" {
		t.Errorf("the other form's field travelled with it: q=%q", got)
	}
}

// A password field is a field like any other to fill in, but the value it holds
// is drawn as dots: the reader paints on a screen other people can see.
func TestBrowserPasswordFieldDrawsDots(t *testing.T) {
	browserEditURLStubFetcher(t)
	config, _, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	doc := parseWebPage(`<body><form action="/login"><input type="password" name="pw" value="hunter2"></form></body>`, "", "https://example.com/login")
	setBrowserDocument(elem.Variable, doc)

	var field *webControl
	for _, ctrl := range browserFormControls(elem) {
		if ctrl.Kind == "text" {
			field = ctrl
		}
	}
	if field == nil {
		t.Fatal("the password field did not render")
	}
	if !field.Secret {
		t.Error("a password field was not marked secret")
	}
	if got := field.Display(); strings.Contains(got, "hunter2") {
		t.Errorf("the password is drawn in the clear: %q", got)
	}
	if got := strings.Join(browserLayoutControlTexts(config, elem), "|"); strings.Contains(got, "hunter2") {
		t.Errorf("the laid-out page shows the password: %q", got)
	}
	// It is still the value that gets submitted.
	if got := field.Value; got != "hunter2" {
		t.Errorf("the field holds %q, want the page's value", got)
	}
}

func TestBrowserFormBoxMatchesFocusAndTapGeometry(t *testing.T) {
	config := layoutTestConfig(t)
	elem := browserCursorTestElement("box_geometry")
	doc := parseWebPage(`<body><input name="q" placeholder="Search" style="width: 220px; height: 70px"></body>`, "", "https://example.com/")
	setBrowserDocument(elem.Variable, doc)
	t.Cleanup(func() { setBrowserDocument(elem.Variable, nil); resetBrowserCursorForTest() })
	layout := browserPageLayout(config, elem)
	if layout == nil || len(layout.Links) != 1 {
		t.Fatal("missing input")
	}
	hit := layout.Links[0]
	if hit.Box.W <= 0 || hit.Box.H <= layout.RowH || hit.W != hit.Box.W {
		t.Fatalf("incorrect input box: %+v", hit)
	}
	rects := browserPageLinkRects(config, elem, layout)
	left, top, _, _ := browserPageContentBox(elem)
	want := sdl.Rect{X: left + hit.Box.X, Y: top + hit.Box.Y - int32(textBrowserScrollY)*layout.RowH, W: hit.Box.W, H: hit.Box.H}
	if rects[0] != want {
		t.Fatalf("tap rect = %+v, want %+v", rects[0], want)
	}
}

// A page can carry two search forms that post to one address. They are two
// forms, not one: pressing the first one's button must not send the second
// one's field, which is what matching them by action alone did.
func TestBrowserFormsWithTheSameActionStaySeparate(t *testing.T) {
	fetches := browserFormFetches(t)
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	const html = `<html><body>
<form action="/search"><input type="text" name="site_q" placeholder="This site"><input type="submit" value="Find here"></form>
<form action="/search"><input type="text" name="web_q" placeholder="The web"><input type="submit" value="Find on the web"></form>
</body></html>`
	setBrowserDocument(elem.Variable, parseWebPage(html, "", "https://example.com/page"))

	var siteField, webField, siteButton *webControl
	for _, ctrl := range browserFormControls(elem) {
		switch {
		case ctrl.Kind == "text" && ctrl.Name == "site_q":
			siteField = ctrl
		case ctrl.Kind == "text" && ctrl.Name == "web_q":
			webField = ctrl
		case ctrl.Kind == "submit" && ctrl.Value == "Find here":
			siteButton = ctrl
		}
	}
	if siteField == nil || webField == nil || siteButton == nil {
		t.Fatalf("page controls missing: site=%v web=%v button=%v", siteField, webField, siteButton)
	}
	if siteField.Form.ID == webField.Form.ID {
		t.Fatalf("two forms share the ID %d", siteField.Form.ID)
	}
	siteField.Value = "cats"

	browserActivatePageControl(renderer, config, elem, siteButton)

	_, query := browserFormQueryOf(t, fetches)
	if got := query.Get("site_q"); got != "cats" {
		t.Errorf("site_q = %q, want the typed value", got)
	}
	if got, ok := query["web_q"]; ok {
		t.Errorf("the other form's field was sent: %v", got)
	}
}

// A field whose closing tag never came ends at the next element, so a malformed
// page does not lose its content into one field's value.
func TestBrowserUnclosedFieldDoesNotSwallowThePage(t *testing.T) {
	browserEditURLStubFetcher(t)
	config, _, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	const html = `<html><body><form action="/go">
<textarea name="note">draft
<p>A paragraph that must still be readable.</p>
</form></body></html>`
	setBrowserDocument(elem.Variable, parseWebPage(html, "", "https://example.com/page"))

	layout := browserPageLayout(config, elem)
	if layout == nil {
		t.Fatal("the page did not lay out")
	}
	var drawn []string
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			drawn = append(drawn, run.Text)
		}
	}
	page := strings.Join(drawn, " ")
	if !strings.Contains(page, "still be readable") {
		t.Errorf("the paragraph was swallowed by the unclosed field: %q", page)
	}
	if strings.Contains(page, "form>") || strings.Contains(page, "p>") {
		t.Errorf("the field kept the page's markup: %q", page)
	}
}

// A form's hidden inputs are not drawn but are still submitted: a search page
// that wants `ie=utf-8` or a site key rejects a query without them.
func TestBrowserHiddenFieldsTravelWithTheForm(t *testing.T) {
	fetches := browserFormFetches(t)
	config, renderer, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	const html = `<html><body><form action="/lite/">
<input type="hidden" name="ie" value="utf-8">
<input type="hidden" name="kl" value="wt-wt">
<input type="text" name="q" placeholder="Search">
<input type="text" name="kl" value="us-en">
<input type="submit" value="Go">
</form></body></html>`
	setBrowserDocument(elem.Variable, parseWebPage(html, "", "https://lite.duckduckgo.com/lite/"))

	if got := browserFormControls(elem); len(got) != 3 {
		t.Fatalf("hidden inputs were drawn: %d controls", len(got))
	}
	var button *webControl
	for _, ctrl := range browserFormControls(elem) {
		if ctrl.Kind == "text" && ctrl.Name == "q" {
			ctrl.Value = "cats"
		}
		if ctrl.Kind == "submit" {
			button = ctrl
		}
	}
	if button == nil {
		t.Fatal("no submit button")
	}

	browserActivatePageControl(renderer, config, elem, button)

	_, query := browserFormQueryOf(t, fetches)
	if got := query.Get("ie"); got != "utf-8" {
		t.Errorf("ie = %q, want the hidden field's value", got)
	}
	if got := query.Get("q"); got != "cats" {
		t.Errorf("q = %q, want the typed value", got)
	}
	// The visible field named kl wins over the hidden one of the same name.
	if got := query["kl"]; len(got) != 1 || got[0] != "us-en" {
		t.Errorf("kl = %v, want only the visible field's value", got)
	}
}

// An empty field's prompt is dimmed toward the page rather than drawn in the
// body colour, which is the only "this is a placeholder" cue the reader can
// paint; it has one face, so it cannot italicise one.
func TestBrowserEmptyFieldPromptIsDimmed(t *testing.T) {
	browserEditURLStubFetcher(t)
	config, _, elem, _, _ := browserEditURLFixture(t)
	defer resetBrowserEditURLState()

	setBrowserDocument(elem.Variable, browserFormDoc())
	field := browserFormControl(t, elem, "text", "q")

	prompt := browserControlColour(config, elem, field)
	field.Value = "cats"
	doc := browserDocumentFor(elem.Variable)
	doc.invalidateFormState()
	filled := browserControlColour(config, elem, field)

	if prompt == filled {
		t.Fatalf("the prompt and a filled value draw in the same colour %v", prompt)
	}
	if prompt.R <= filled.R || prompt.G <= filled.G || prompt.B <= filled.B {
		t.Errorf("prompt %v is not lighter than the value %v", prompt, filled)
	}
}

// browserControlColour is the colour the page draws a control's run in.
func browserControlColour(config *Config, elem Element, ctrl *webControl) sdl.Color {
	layout := browserPageLayout(config, elem)
	if layout == nil {
		return sdl.Color{}
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Control == ctrl {
				return run.Color
			}
		}
	}
	return sdl.Color{}
}

// Every glyph a control paints has to exist in the bundled face. A missing one
// is replaced by the font sanitiser, so a tick box drawn with the ballot glyph
// (U+2610 is not in Inter) came out on the device as "…" instead of a box.
func TestBrowserControlGlyphsAreInTheFont(t *testing.T) {
	testInterFont(t, 16)
	controls := []*webControl{
		{Kind: "check", Checked: true},
		{Kind: "check"},
		{Kind: "radio", Checked: true},
		{Kind: "radio"},
		{Kind: "select", Options: []string{"One"}},
		{Kind: "select"},
		{Kind: "text", Placeholder: "Search"},
		{Kind: "text"},
		{Kind: "text", Value: "cats"},
		{Kind: "text", Value: "hunter2", Secret: true},
		{Kind: "textarea", Placeholder: "Note"},
		{Kind: "submit", Value: "Send"},
		{Kind: "submit"},
	}
	for _, ctrl := range controls {
		shown := ctrl.Display()
		if strings.TrimSpace(shown) == "" {
			t.Errorf("a %s control drew nothing", ctrl.Kind)
		}
		for _, r := range shown {
			if r == ' ' {
				continue
			}
			if !fontCoversRune("Inter-Regular.ttf", r) {
				t.Errorf("%s control draws %q (U+%04X), which Inter does not have", ctrl.Kind, r, r)
			}
		}
	}
}

// browserLayoutControlTexts is what the page currently draws for its controls,
// which is where a stepped select or a ticked box has to show its new state.
func browserLayoutControlTexts(config *Config, elem Element) []string {
	layout := browserPageLayout(config, elem)
	if layout == nil {
		return nil
	}
	var out []string
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Control != nil {
				out = append(out, run.Text)
			}
		}
	}
	return out
}
