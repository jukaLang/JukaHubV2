package main

// webpage_css_import_test.go covers the stylesheet chain a real site uses:
// a linked sheet importing its base and theme sheets. These tests serve the
// sheets from httptest, run the browser's own fetcher, then parse the page with
// the returned CSS so the assertion is about the style the reader actually
// applies, not merely that a string was fetched.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func webImportTestServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range files {
		body := body
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(path, ".css") {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			}
			_, _ = w.Write([]byte(body))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func webImportBlock(doc *webDocument, text string) (webBlock, bool) {
	if doc == nil {
		return webBlock{}, false
	}
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			if strings.Contains(run.Text, text) {
				return block, true
			}
		}
	}
	return webBlock{}, false
}

func TestWebLinkedStylesheetImportsApply(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/main.css": `@import url("base.css"); .hero{color:#cc0000}`,
		"/base.css": `.hero{background:#abcdef}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	html := `<link rel="stylesheet" href="/main.css"><div class="hero">Imported appearance</div>`
	css := fetchWebStylesheets(html, base)
	if !strings.Contains(css, "background:#abcdef") {
		t.Fatalf("imported base rule missing from fetched CSS: %q", css)
	}
	if strings.Index(css, "background:#abcdef") > strings.Index(css, "color:#cc0000") {
		t.Fatalf("imported rules came after the importing sheet: %q", css)
	}
	doc := parseWebPage(html, css, base.String())
	block, ok := webImportBlock(doc, "Imported appearance")
	if !ok {
		t.Fatalf("page block missing; blocks=%d", len(doc.Blocks))
	}
	if !block.Style.HasBg || block.Style.Background != (sdl.Color{R: 171, G: 205, B: 239, A: 255}) {
		t.Fatalf("imported background was not applied: bg=%v has=%v", block.Style.Background, block.Style.HasBg)
	}
	if !block.Style.HasColor || block.Style.Color != (sdl.Color{R: 204, G: 0, B: 0, A: 255}) {
		t.Fatalf("importing sheet's colour was not applied: colour=%v has=%v", block.Style.Color, block.Style.HasColor)
	}
}

func TestWebStylesheetImportRebasesAgainstImportedSheet(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/css/main.css":       `@import "base/theme.css";`,
		"/css/base/theme.css": `.hero{background-image:url("../img/hero.png")}`,
	})
	base, err := url.Parse(srv.URL + "/css/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	css := fetchWebStylesheets(`<link rel="stylesheet" href="/css/main.css">`, base)
	want := srv.URL + "/css/img/hero.png"
	if !strings.Contains(css, want) {
		t.Fatalf("imported url() was not rebased against its own sheet: want %q in %q", want, css)
	}
}

func TestWebStylesheetImportHonoursMedia(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/main.css": `@import "wide.css" screen and (min-width: 1px);`,
		"/wide.css": `.hero{color:#123456}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	css := fetchWebStylesheets(`<link rel="stylesheet" href="/main.css">`, base)
	if !strings.Contains(css, "@media screen and (min-width: 1px)") {
		t.Fatalf("media-qualified import lost its media wrapper: %q", css)
	}
	if !strings.Contains(css, "color:#123456") {
		t.Fatalf("media-qualified import body missing: %q", css)
	}
}

func TestWebStylesheetImportCycleTerminates(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/a.css": `@import "b.css"; .a{color:#111}`,
		"/b.css": `@import "a.css"; .b{color:#222}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	css := fetchWebStylesheets(`<link rel="stylesheet" href="/a.css">`, base)
	if !strings.Contains(css, "color:#111") || !strings.Contains(css, "color:#222") {
		t.Fatalf("cycle lost one side of the sheet chain: %q", css)
	}
}

func webInlineImportedDoc(t *testing.T, html string, base *url.URL) *webDocument {
	t.Helper()
	expanded := webExpandInlineCSSImports(html, base)
	return parseWebPage(expanded, "", base.String())
}

func TestWebInlineStyleImportsApply(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/base.css": `.hero{background:#abcdef}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	html := `<style>@import "base.css"; .hero{color:#cc0000}</style><div class="hero">Inline imported appearance</div>`
	doc := webInlineImportedDoc(t, html, base)
	block, ok := webImportBlock(doc, "Inline imported appearance")
	if !ok {
		t.Fatalf("inline import page block missing; blocks=%d", len(doc.Blocks))
	}
	if !block.Style.HasBg || block.Style.Background != (sdl.Color{R: 171, G: 205, B: 239, A: 255}) {
		t.Fatalf("inline imported background was not applied: bg=%v has=%v", block.Style.Background, block.Style.HasBg)
	}
	if !block.Style.HasColor || block.Style.Color != (sdl.Color{R: 204, G: 0, B: 0, A: 255}) {
		t.Fatalf("declaring inline rule was not applied: colour=%v has=%v", block.Style.Color, block.Style.HasColor)
	}
}

func TestWebInlineStyleImportKeepsPerBlockCascadeOrder(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/base.css": `.hero{background:#abcdef}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	// The second block's import must override the first block's background.
	// Gathering every inline import at the page top would let the first block
	// win instead, which is the ordering bug this fixture pins.
	html := `<style>.hero{background:#111111}</style>` +
		`<style>@import "base.css"; .hero{color:#cc0000}</style>` +
		`<div class="hero">Inline cascade</div>`
	doc := webInlineImportedDoc(t, html, base)
	block, ok := webImportBlock(doc, "Inline cascade")
	if !ok {
		t.Fatalf("inline cascade block missing; blocks=%d", len(doc.Blocks))
	}
	if block.Style.Background != (sdl.Color{R: 171, G: 205, B: 239, A: 255}) {
		t.Fatalf("inline imports lost their block position: bg=%v", block.Style.Background)
	}
}

func TestWebInlineStyleImportRebasesRelativeURLs(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/css/base/theme.css": `.hero{background-image:url("../img/hero.png")}`,
	})
	base, err := url.Parse(srv.URL + "/css/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	html := `<style>@import "base/theme.css";</style><div class="hero">Relative inline</div>`
	expanded := webExpandInlineCSSImports(html, base)
	want := srv.URL + "/css/img/hero.png"
	if !strings.Contains(expanded, want) {
		t.Fatalf("inline imported url() was not rebased: want %q in %q", want, expanded)
	}
}

func TestWebInlineStyleImportHonoursMedia(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/wide.css": `.hero{color:#123456}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	html := `<style>@import "wide.css" screen and (min-width: 1px);</style><div class="hero">Inline media</div>`
	expanded := webExpandInlineCSSImports(html, base)
	if !strings.Contains(expanded, "@media screen and (min-width: 1px)") {
		t.Fatalf("inline media import lost its wrapper: %q", expanded)
	}
	doc := parseWebPage(expanded, "", base.String())
	block, ok := webImportBlock(doc, "Inline media")
	if !ok {
		t.Fatalf("inline media block missing; blocks=%d", len(doc.Blocks))
	}
	if block.Style.Color != (sdl.Color{R: 18, G: 52, B: 86, A: 255}) {
		t.Fatalf("media-qualified inline import was not applied: colour=%v", block.Style.Color)
	}
}

func TestWebInlineStyleImportCycleTerminates(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/a.css": `@import "b.css"; .a{color:#111}`,
		"/b.css": `@import "a.css"; .b{color:#222}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	html := `<style>@import "a.css";</style><p>Inline cycle</p>`
	expanded := webExpandInlineCSSImports(html, base)
	if !strings.Contains(expanded, "color:#111") || !strings.Contains(expanded, "color:#222") {
		t.Fatalf("inline import cycle lost a sheet: %q", expanded)
	}
}

func TestWebInlineStyleCommentedImportIsIgnored(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/missing.css": `.missing{color:#fedcba}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	html := `<style>/* @import "missing.css"; */ .hero{color:#123456}</style><p>Inline comment</p>`
	expanded := webExpandInlineCSSImports(html, base)
	if strings.Contains(expanded, "color:#fedcba") {
		t.Fatalf("commented inline import was fetched: %q", expanded)
	}
	if !strings.Contains(expanded, "color:#123456") {
		t.Fatalf("inline sheet body missing: %q", expanded)
	}
}

func TestWebStylesheetCommentedImportIsIgnored(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/main.css":    `/* @import "missing.css"; */ .hero{color:#123456}`,
		"/missing.css": `.missing{color:#fedcba}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatalf("parse test base: %v", err)
	}
	css := fetchWebStylesheets(`<link rel="stylesheet" href="/main.css">`, base)
	if strings.Contains(css, "color:#fedcba") {
		t.Fatalf("commented-out import was fetched: %q", css)
	}
	if !strings.Contains(css, "color:#123456") {
		t.Fatalf("sheet body missing: %q", css)
	}
}

// A page's web fonts are outside data the reader unpacks: @font-face sources
// it can read, the subsets worth a download, and a WOFF file rebuilt table for
// table as the sfnt it packs (webpage_webfont.go).
func TestWebFontFacesAndWOFF(t *testing.T) {
	p := &webParser{doc: &webDocument{URL: "https://example.com/css/site.css"}}
	got := p.webFontSources(`url(a.woff2) format("woff2"), url('a.woff') format('woff'), local(Foo), url(b.ttf), url(c.eot?#iefix) format("embedded-opentype"), url(d.svg#x)`)
	want := []string{"https://example.com/css/a.woff", "https://example.com/css/b.ttf"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("sources = %q, want %q", got, want)
	}
	for v, useful := range map[string]bool{
		"": true, "U+0000-00FF, U+0131": true, "U+0400-045F": false, "U+F000-F2FF": true, "U+00??": true, "U+1F00-1FFF": false,
	} {
		if webFontRangeUseful(v) != useful {
			t.Errorf("unicode-range %q useful = %v", v, !useful)
		}
	}
	if lo, hi := webFontWeightRange("100 900"); lo != 100 || hi != 900 {
		t.Errorf("weight range = %d..%d", lo, hi)
	}

	ttf := libThemeSerifTTF
	if len(ttf) < 12 {
		t.Skip("no embedded serif face")
	}
	be := binary.BigEndian
	n := int(be.Uint16(ttf[4:6]))
	var head, body bytes.Buffer
	head.Write(make([]byte, 44+20*n))
	woffHead := head.Bytes()
	for i := 0; i < n; i++ {
		rec := ttf[12+16*i : 28+16*i]
		off, length := be.Uint32(rec[8:12]), be.Uint32(rec[12:16])
		table := ttf[off : off+length]
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		_, _ = zw.Write(table)
		_ = zw.Close()
		packed := z.Bytes()
		if len(packed) >= len(table) {
			packed = table
		}
		e := woffHead[44+20*i : 64+20*i]
		copy(e[0:4], rec[0:4])
		be.PutUint32(e[4:8], uint32(44+20*n+body.Len()))
		be.PutUint32(e[8:12], uint32(len(packed)))
		be.PutUint32(e[12:16], length)
		be.PutUint32(e[16:20], be.Uint32(rec[4:8]))
		body.Write(packed)
		for body.Len()%4 != 0 {
			body.WriteByte(0)
		}
	}
	copy(woffHead[0:4], "wOFF")
	copy(woffHead[4:8], ttf[0:4])
	be.PutUint16(woffHead[12:14], uint16(n))
	be.PutUint32(woffHead[16:20], uint32(len(ttf)))
	woff := append(append([]byte(nil), woffHead...), body.Bytes()...)
	sfnt, ok := webFontSFNT(woff, 8<<20)
	if !ok {
		t.Fatal("the WOFF did not unpack")
	}
	tables := func(f []byte) map[string]string {
		out := map[string]string{}
		for i := 0; i < int(be.Uint16(f[4:6])); i++ {
			rec := f[12+16*i : 28+16*i]
			off, length := be.Uint32(rec[8:12]), be.Uint32(rec[12:16])
			out[string(rec[0:4])] = string(f[off : off+length])
		}
		return out
	}
	a, b := tables(ttf), tables(sfnt)
	if len(a) != len(b) {
		t.Fatalf("%d tables back, want %d", len(b), len(a))
	}
	for tag, data := range a {
		if b[tag] != data {
			t.Errorf("table %q differs after unpacking", tag)
		}
	}
	if _, ok := webFontSFNT([]byte("wOF2\x00\x01\x00\x00 not a font"), 8<<20); ok {
		t.Error("a WOFF2 file was taken for a font the reader can open")
	}
}
