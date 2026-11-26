package main

// webpage_css_import_test.go covers the stylesheet chain a real site uses:
// a linked sheet importing its base and theme sheets. These tests serve the
// sheets from httptest, run the browser's own fetcher, then parse the page with
// the returned CSS so the assertion is about the style the reader actually
// applies, not merely that a string was fetched.

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
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
	want := []string{"https://example.com/css/a.woff2", "https://example.com/css/a.woff", "https://example.com/css/b.ttf"}
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
		t.Error("a broken WOFF2 file was taken for a font the reader can open")
	}
}

// A WOFF2 font is Brotli-packed outside data too: a stream from the reference
// encoder decodes to its text (dictionary words included), and a subset of
// Liberation Sans with glyf, loca and hmtx transformed (two composite glyphs)
// rebuilds to exactly the sfnt the Python twin of webfont_woff2.go produced,
// whose outlines and metrics matched fontTools' own WOFF2 reader.
func TestWebFontWOFF2(t *testing.T) {
	decode := func(s string) []byte {
		data, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	sum := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	text, err := webBrotliDecode(decode(webTestBrotliStream), 1<<20)
	if err != nil || len(text) != 1090 || sum(text) != "c513e220ba7cbf86316eca2d8ff15b9056944d3149e47a08af0209d6b220cc43" {
		t.Fatalf("Brotli stream decoded to %d bytes (%v), sha256 %s", len(text), err, sum(text))
	}
	sfnt, ok := webFontSFNT(decode(webTestWOFF2), 8<<20)
	if !ok || sum(sfnt) != "60336a49abf95c6b4b897721e2494f6f65e434ec35c3a02ad4891ebd28294959" {
		t.Fatalf("WOFF2 rebuilt to %d bytes (ok %v), sha256 %s", len(sfnt), ok, sum(sfnt))
	}
	if _, ok := webFontSFNT(append(decode(webTestWOFF2)[:200:200], make([]byte, 64)...), 8<<20); ok {
		t.Error("a cut WOFF2 file was taken for a font")
	}
}

const webTestBrotliStream = `
G0EEIJwJdqyVgWmdFyMkmR04J4gMISsfg/NKzjvFg7y9vrdgrLy8oFmBxfYG/+kJWkBs3u/9JtWC
VbCTywHggHmqKwZFt3l13CplsUlg2gF4BiyAJtg+FhCg+8pvgXZXga3aFqjvJSCgcqhAuAk2ngPf
6Cr8iWMomgLBgpcWpGTRINba53NymI48qnRqR4Aq5vB2RrTP6KGuCSq6BwWZi/YLrsB56rwl98HG
YPWmOJOdac8B8N9BXG6P1+cPBEPhSDQWTyRT6Uw2ly8US+VKtVZvNFvtThft9QfD0XgyxWbzxXK1
3uDb3f5wPJ0vV4KkaIbleEGUZEXVdMO0bOd2fzxf78/39wchGEExnCApmmE5XhAlWVE13TAt23E9
PwijOEmzvCirumm7fhineVm3/Tiv+3k/398fQDCCYjhBUjTDcrwgSrKiarphWrbjen4QRnGSZnlR
VnXTdv0wTvOybvtxXvfzfn/pqeCBARTO3kqZQhlCuq3Xfr0rwFgIIRVIBg==
`

const webTestWOFF2 = `
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
gD1IuIjbkHDjyzApDS+cEsRvWwAAAAA=
`
