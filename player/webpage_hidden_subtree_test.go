package main

import (
	"strings"
	"testing"
)

// CNN ships a full-screen ad feedback form at the start of its body with
// style="display:none". The text was skipped, but its modal boxes and radio
// controls still painted over the news page.
func TestHiddenSubtreeDoesNotPaintBoxesOrControls(t *testing.T) {
	h := newHeadlessBrowser(t)
	html := `<style>
		.modal { display:flex; position:fixed; inset:0; background:#111; }
		.panel { width:700px; height:600px; background:#bbb; }
	</style>
	<div class="modal" style="display:none">
		<div class="panel"><form>
			<h3>Hidden feedback dialog</h3>
			<input type="radio" name="rating" value="1">
			<label><input type="checkbox" name="issue">Hidden issue</label>
			<img src="` + headlessPixel + `" alt="Hidden picture">
			<svg width="500" height="300"><rect width="500" height="300"/></svg>
			<iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe>
		</form></div>
	</div>
	<h1>Latest headlines</h1><p>Visible story</p>`
	doc, _ := h.load("hidden_subtree", html)
	for _, b := range doc.Blocks {
		if b.Image || b.Video || b.Placeholder {
			t.Fatalf("hidden media produced a page block: %+v", b)
		}
		for _, run := range b.Runs {
			if run.Style.Control != nil || strings.Contains(run.Text, "Hidden") {
				t.Fatalf("hidden dialog produced a page run: %+v", run)
			}
		}
	}
	for _, box := range doc.Boxes {
		if box.style.Hidden {
			t.Fatal("hidden modal produced a painted box")
		}
	}
	img := h.rasterize("hidden_subtree")
	if got := headlessPixelAt(img, 500, 300); !headlessNear(got, doc.Paper, 2) {
		t.Fatalf("hidden modal covered the page: pixel at (500,300) = %+v, paper = %+v", got, doc.Paper)
	}
}

func TestHiddenFormsDoNotConsumeVisibleContent(t *testing.T) {
	doc := parseWebPage(`<div hidden><form><input type="radio"><button>Hidden button</button></form></div>`+
		`<section aria-hidden="true"><div><input type="checkbox"></div></section>`+
		`<svg style="display:none" width="500" height="300"><rect/></svg>`+
		`<video style="display:none" src="https://example.com/clip.mp4"></video>`+
		`<p>Visible after hidden forms</p>`, "", "https://fixture.jukahub.test/")
	if len(doc.Blocks) != 1 || len(doc.Blocks[0].Runs) != 1 || doc.Blocks[0].Runs[0].Text != "Visible after hidden forms" {
		t.Fatalf("hidden forms leaked into the visible page: %+v", doc.Blocks)
	}
}

func TestHiddenWrapperKeepsFormToken(t *testing.T) {
	doc := parseWebPage(`<form action="/search"><div style="display:none">`+
		`<input type="hidden" name="csrf" value="token"></div>`+
		`<input name="query"></form>`, "", "https://fixture.jukahub.test/")
	fields := doc.FormHidden[1]
	if len(fields) != 1 || fields[0].Name != "csrf" || fields[0].Value != "token" {
		t.Fatalf("form lost its hidden token: %+v", fields)
	}
}

func TestInvisibleFixedMenuDoesNotPushPageContentDown(t *testing.T) {
	doc := parseWebPage(`<style>.menu{position:fixed;visibility:hidden;min-height:100vh}</style>`+
		`<nav class="menu"><ul><li>Hidden menu item</li><li>Another hidden item</li></ul></nav>`+
		`<main><h1>Visible article</h1></main>`, "", "https://example.com/")
	if len(doc.Blocks) != 1 || len(doc.Blocks[0].Runs) != 1 || doc.Blocks[0].Runs[0].Text != "Visible article" {
		t.Fatalf("closed menu occupied page flow: %+v", doc.Blocks)
	}
}
