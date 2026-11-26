package main

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// A <link> sheet the page's load fetched reaches its scripts: getComputedStyle
// reports the display it sets, so a jQuery-style show() shows the panel.
func TestWebJSLinkedSheetReachesScripts(t *testing.T) {
	base, err := url.Parse("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	gen := webJSLoadGeneration()
	webJSSetSheetSource(gen, func(page, address string) string {
		if address == "https://example.com/site.css" {
			return ".panel{display:none}"
		}
		return ""
	})
	defer webJSSetSheetSource(gen, nil)
	source := `<html><head><link rel="stylesheet" href="/site.css"></head><body>` +
		`<div class="panel" id="p">Panel</div><script>var p = document.getElementById('p');` +
		`document.body.setAttribute('data-before', getComputedStyle(p).display);` +
		`if (getComputedStyle(p).display === 'none') { p.style.display = 'block'; }</script></body></html>`
	scripted := webJSRunPageFor(source, base, gen, time.Time{})
	if !strings.Contains(scripted.HTML, `data-before="none"`) {
		t.Fatalf("getComputedStyle did not see the linked sheet (%s): %s", scripted.Reason, scripted.HTML)
	}
	if !strings.Contains(scripted.HTML, "display: block") && !strings.Contains(scripted.HTML, "display:block") {
		t.Fatalf("show() did not show the panel the linked sheet hides: %s", scripted.HTML)
	}
}

func TestWebHasReflectsScriptedMutations(t *testing.T) {
	cases := []struct {
		name   string
		source string
		text   string
		want   bool
	}{
		{
			name:   "script adds matching child",
			source: `<style>.card:has(> img){color:#cc0000}</style><div class="card"><span>Hit</span></div><script>var img = document.createElement('img'); document.querySelector('.card').appendChild(img);</script>`,
			text:   "Hit",
			want:   true,
		},
		{
			name:   "script adds matching child through innerHTML",
			source: `<style>.card:has(> img){color:#cc0000}</style><div class="card" id="card"><span>Hit</span></div><script>document.getElementById('card').insertAdjacentHTML('beforeend', '<img src="x.png">');</script>`,
			text:   "Hit",
			want:   true,
		},
		{
			name:   "script adds matching class",
			source: `<style>.card:has(> .badge){color:#cc0000}</style><div class="card"><span>Hit</span><i></i></div><script>document.querySelector('.card i').className = 'badge';</script>`,
			text:   "Hit",
			want:   true,
		},
		{
			name:   "script adds matching next sibling",
			source: `<style>.first:has(+ .second){color:#cc0000}</style><div class="first">Hit</div><div></div><script>document.querySelector('.first').nextElementSibling.className = 'second';</script>`,
			text:   "Hit",
			want:   true,
		},
		{
			name:   "script removes matching child",
			source: `<style>.card:has(> img){color:#cc0000}</style><div class="card"><span>Miss</span><img src="x.png"></div><script>document.querySelector('.card img').remove();</script>`,
			text:   "Miss",
			want:   false,
		},
	}
	base, err := url.Parse("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scripted := webJSRunPage(tc.source, base)
			if scripted.HTML == tc.source {
				t.Fatalf("scripts did not change the page: %q", scripted.Reason)
			}
			doc := parseWebPage(scripted.HTML, "", base.String())
			style, ok := webTestStyleForText(doc, tc.text)
			if !ok {
				t.Fatalf("text %q was not found in the scripted page", tc.text)
			}
			if got := style.Color.R == 204 && style.Color.G == 0 && style.Color.B == 0; got != tc.want {
				t.Fatalf(":has() match after script = %v, want %v; scripted HTML: %s", got, tc.want, scripted.HTML)
			}
		})
	}
}
