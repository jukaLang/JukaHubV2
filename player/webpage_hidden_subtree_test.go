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

// Ads are cut out of the markup before the reader parses it
// (webpage_adremove.go): the slot, its label and the frame round it go, and
// nothing else does — a classifieds listing called "ad-card", a sentence that
// says "advertisement", a page flagged "has-ads", a story an unclosed ad tag
// runs into all stay.
func TestWebRemoveAdsCutsAdsNotContent(t *testing.T) {
	cases := []struct {
		name, html string
		gone, kept []string
	}{
		{"slot, label and frame",
			`<div class="story"><p>One two three</p><div class="ad-container"><div class="ad-label">Advertisement</div>` +
				`<div id="div-gpt-ad-1234567890-0" style="min-width:300px;min-height:250px;"><script>googletag.cmd.push(function(){` +
				`googletag.display('div-gpt-ad-1234567890-0');});</script></div></div><p>Four five six</p></div>`,
			[]string{"ad-container", "Advertisement", "div-gpt-ad"}, []string{"One two three", "Four five six", webAdCutMarker}},
		{"adsense",
			`<p>Before para</p><ins class="adsbygoogle" style="display:block" data-ad-client="ca-pub-1" data-ad-slot="2"` +
				` data-ad-format="auto"></ins><p>After para</p>`,
			[]string{`class="adsbygoogle"`}, []string{"Before para", "After para"}},
		{"label line between paragraphs",
			"<div class=\"article-body\"><p>First paragraph.</p>\n ADVERTISEMENT\n <div class=\"ad\"></div><p>Second paragraph.</p></div>",
			[]string{"ADVERTISEMENT", `class="ad"`}, []string{"First paragraph.", "Second paragraph.", "article-body"}},
		{"ad server picture and its link",
			`<p>Before</p><a href="https://ad.doubleclick.net/ddm/clk/123"><img src="https://tpc.googlesyndication.com/simgad/123"` +
				` width="300" height="250"></a><p><a href="https://www.nielsen.com/insights/">Nielsen report</a> says</p>`,
			[]string{"doubleclick", "googlesyndication"}, []string{"Before", "Nielsen report"}},
		{"native ad card",
			`<ul><li class="js-stream-content"><div class="controller gemini-ad native-ad-item"><a href="https://shop.example.net/x">` +
				`<h3>Try the new cordless vacuum</h3></a><span>Sponsored</span></div></li><li class="js-stream-content">` +
				`<h3>Real headline story</h3></li></ul>`,
			[]string{"cordless vacuum", "Sponsored"}, []string{"Real headline story"}},
		{"search ads",
			`<table><tr class="result-sponsored"><td>1.&nbsp;</td><td><a rel="nofollow" href="https://duckduckgo.com/y.js?ad_domain=shop.com"` +
				` class="result-link">Buy Shoes Now</a></td></tr><tr><td>1.&nbsp;</td><td><a class="result-link"` +
				` href="https://en.wikipedia.org/wiki/Shoe">Shoe - Wikipedia</a></td></tr></table>`,
			[]string{"Buy Shoes Now"}, []string{"Shoe - Wikipedia"}},
		{"classifieds listings",
			`<div class="ads-list"><div class="ad-card"><img class="ad-photo" src="/p/1.jpg"><h3 class="ad-title">iPhone 13 Pro 128GB</h3>` +
				`<span class="ad-price">$500</span></div></div>`,
			nil, []string{"iPhone 13 Pro", "/p/1.jpg", "$500"}},
		{"page states and sentences",
			`<body class="has-ads ads-enabled"><div class="content with-ads"><p>The <b>advertisement</b> was everywhere.</p></div>` +
				`<div class="ad-free-banner"><p>Go ad-free today</p></div><nav><a href="/ads">Ads</a></nav></body>`,
			nil, []string{"<b>advertisement</b>", "Go ad-free today", ">Ads<"}},
		{"unclosed ad tag",
			`<section><div class="ad"><p>Real story paragraph one.</p><p>Real story two.</p></section><p>after</p>`,
			nil, []string{"Real story paragraph one.", "Real story two."}},
		{"style hashes and look-alike words",
			`<div class="sc-adXcvb adQBCa"><p class="header thread download readMore loadAd">Kept text</p></div>` +
				`<i class="fa fa-ad"></i><span class="fi fi-ad"></span>`,
			nil, []string{"Kept text", "fa-ad", "fi-ad"}},
	}
	for _, c := range cases {
		out, _ := webRemoveAds(c.html, "https://news.example.com/story/1")
		for _, s := range c.gone {
			if strings.Contains(out, s) {
				t.Errorf("%s: %q was not cut: %s", c.name, s, out)
			}
		}
		for _, s := range c.kept {
			if !strings.Contains(out, s) {
				t.Errorf("%s: %q was lost: %s", c.name, s, out)
			}
		}
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
