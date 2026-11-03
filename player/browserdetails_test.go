package main

// browserdetails_test.go — <details> folding.
//
// A page's long logs, changelogs and screenshots are often behind a closed
// <details>. The reader laid out everything, so an issue whose evidence sat in
// one read as the evidence. These tests pin that a closed section is out of the
// layout while its summary (and its toggle) stay on the page, that `open`
// starts expanded, that pressing the marker lays the body out again, and that
// nesting follows the outer section.

import (
	"strings"
	"testing"
)

func parseDetailsPage(t *testing.T) *webDocument {
	t.Helper()
	return parseWebPage(`<html><body>
		<p>before</p>
		<details><summary>Crash log</summary><p>trace line</p></details>
		<details open><summary>Build info</summary><p>go1.27</p></details>
		<p>after</p>
	</body></html>`, "", "https://example.com/issue")
}

// detailsBlockText joins a block's runs, which is the text the layout measures.
func detailsBlockText(b webBlock) string {
	var sb strings.Builder
	for _, run := range b.Runs {
		sb.WriteString(run.Text)
	}
	return sb.String()
}

func detailsFindBlock(doc *webDocument, want string) (webBlock, bool) {
	for _, b := range doc.Blocks {
		if strings.Contains(detailsBlockText(b), want) {
			return b, true
		}
	}
	return webBlock{}, false
}

func TestClosedDetailsHidesItsBody(t *testing.T) {
	doc := parseDetailsPage(t)

	body, ok := detailsFindBlock(doc, "trace line")
	if !ok {
		t.Fatal("the closed section's body is not in the document at all")
	}
	if !body.Hidden() {
		t.Error("a closed <details> laid out its body: the crash log would be drawn in full")
	}

	summary, ok := detailsFindBlock(doc, "Crash log")
	if !ok {
		t.Fatal("the closed section's summary is missing")
	}
	if summary.Hidden() {
		t.Error("the summary of a closed <details> was hidden, so there is nothing to open")
	}
	if text := detailsBlockText(summary); !strings.Contains(text, "[+]") {
		t.Errorf("closed summary %q does not name its toggle", text)
	}
}

func TestOpenDetailsShowsItsBody(t *testing.T) {
	doc := parseDetailsPage(t)
	body, ok := detailsFindBlock(doc, "go1.27")
	if !ok {
		t.Fatal("the open section's body is missing")
	}
	if body.Hidden() {
		t.Error("a <details open> section hid its body")
	}
	summary, _ := detailsFindBlock(doc, "Build info")
	if text := detailsBlockText(summary); !strings.Contains(text, "[-]") {
		t.Errorf("open summary %q does not show it is open", text)
	}
}

func TestDetailsToggleLaysTheBodyOutAgain(t *testing.T) {
	doc := parseDetailsPage(t)

	var toggle *webControl
	for _, b := range doc.Blocks {
		for _, run := range b.Runs {
			if c := run.Style.Control; c != nil && c.Kind == "details" && !c.Checked {
				toggle = c
			}
		}
	}
	if toggle == nil {
		t.Fatal("the closed section's toggle is not on the page")
	}

	browserActivatePageControl(nil, &Config{}, Element{}, toggle)

	if !toggle.Checked {
		t.Fatal("pressing the marker did not open the section")
	}
	if got := toggle.Display(); got != " [-] " {
		t.Errorf("marker reads %q after opening, want \" [-] \"", got)
	}
	body, _ := detailsFindBlock(doc, "trace line")
	if body.Hidden() {
		t.Error("the opened section's body is still out of the layout")
	}

	// And closing it again is the same press, one state back.
	browserActivatePageControl(nil, &Config{}, Element{}, toggle)
	if toggle.Checked {
		t.Error("pressing the marker again did not close the section")
	}
	body, _ = detailsFindBlock(doc, "trace line")
	if !body.Hidden() {
		t.Error("the closed section's body came back without its marker")
	}
}

func TestBedBodyStaysHiddenInsideAClosedSection(t *testing.T) {
	doc := parseWebPage(`<body><details><summary>outer</summary>
		<details open><summary>inner</summary><p>inner body</p></details>
	</details></body>`, "", "https://example.com/")

	body, ok := detailsFindBlock(doc, "inner body")
	if !ok {
		t.Fatal("the nested body is missing from the document")
	}
	if !body.Hidden() {
		t.Error("a section marked open inside a closed section was laid out")
	}
}

func TestPagesWithoutDetailsAreUntouched(t *testing.T) {
	doc := parseWebPage(`<body><p>plain paragraph</p></body>`, "", "https://example.com/")
	for _, b := range doc.Blocks {
		if b.Hidden() {
			t.Fatal("a page with no <details> hid one of its blocks")
		}
		for _, run := range b.Runs {
			if run.Style.Control != nil {
				t.Errorf("a plain paragraph grew a control: %q", run.Text)
			}
		}
	}
}
